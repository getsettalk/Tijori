// Package s3api implements the S3-compatible HTTP API on top of the storage
// backend: routing, authorisation, XML responses and S3 error codes.
package s3api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/getsettalk/Tijori/internal/auth"
	"github.com/getsettalk/Tijori/internal/quota"
	"github.com/getsettalk/Tijori/internal/s3err"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
)

// Events receives notifications about changes, used by video processing.
type Events interface {
	ObjectCreated(bucket, key string, info storage.ObjectInfo)
}

// Handler serves the S3 API.
type Handler struct {
	Store    *storage.Store
	Verifier *auth.Verifier
	Quota    *quota.Manager
	Region   string
	Log      *slog.Logger
	Events   Events
	// ErrorFormat is "auto", "xml" or "json" (see config.Config).
	ErrorFormat string

	Stats Stats
}

// Stats are simple counters exposed on /metrics.
type Stats struct {
	Requests  atomic.Int64
	Errors    atomic.Int64
	BytesIn   atomic.Int64
	BytesOut  atomic.Int64
	Uploads   atomic.Int64
	Downloads atomic.Int64
}

type action int

const (
	actListBuckets action = iota
	actRead
	actWrite
	actAdmin // create / delete buckets
)

type request struct {
	w      http.ResponseWriter
	r      *http.Request
	bucket string
	key    string
	q      url.Values
	auth   *auth.Result
	id     string
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

// SplitPath turns "/bucket/some/key" into ("bucket", "some/key").
func SplitPath(p string) (string, string) {
	p = strings.TrimPrefix(p, "/")
	bucket, key, _ := strings.Cut(p, "/")
	return bucket, key
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.Stats.Requests.Add(1)
	req := &request{w: w, r: r, q: r.URL.Query(), id: newRequestID()}
	req.bucket, req.key = SplitPath(r.URL.Path)
	w.Header().Set("x-amz-request-id", req.id)
	w.Header().Set("Server", "Tijori")

	res, err := h.Verifier.Verify(r)
	if err != nil {
		h.fail(req, err)
		return
	}
	req.auth = res
	r.Body = res.Body
	if err := h.route(req); err != nil {
		h.fail(req, err)
	}
}

func (h *Handler) route(req *request) error {
	r, q := req.r, req.q
	if req.bucket == "" {
		if r.Method == http.MethodGet {
			return h.listBuckets(req)
		}
		return s3err.MethodNotAllowed
	}
	if req.key == "" {
		switch r.Method {
		case http.MethodPut:
			if q.Has("acl") {
				return h.putACL(req)
			}
			if hasAny(q, "policy", "cors", "lifecycle", "tagging", "versioning", "encryption", "website", "object-lock", "ownershipControls", "publicAccessBlock", "logging", "notification", "replication", "accelerate", "requestPayment") {
				return s3err.NotImplemented
			}
			return h.createBucket(req)
		case http.MethodDelete:
			if len(q) > 0 {
				return s3err.NotImplemented
			}
			return h.deleteBucket(req)
		case http.MethodHead:
			return h.headBucket(req)
		case http.MethodPost:
			if q.Has("delete") {
				return h.deleteObjects(req)
			}
			return s3err.NotImplemented
		case http.MethodGet:
			switch {
			case q.Has("location"):
				return h.getBucketLocation(req)
			case q.Has("versioning"):
				return h.getBucketVersioning(req)
			case q.Has("uploads"):
				return h.listMultipartUploads(req)
			case q.Has("acl"):
				return h.getACL(req)
			case hasAny(q, "policy", "cors", "lifecycle", "tagging", "encryption", "website", "object-lock", "ownershipControls", "publicAccessBlock", "logging", "notification", "replication", "accelerate", "requestPayment", "versions", "policyStatus", "analytics", "metrics", "inventory", "intelligent-tiering"):
				return s3err.NotImplemented
			case q.Get("list-type") == "2":
				return h.listObjectsV2(req)
			default:
				return h.listObjectsV1(req)
			}
		}
		return s3err.MethodNotAllowed
	}
	if q.Has("acl") {
		switch r.Method {
		case http.MethodGet:
			return h.getACL(req)
		case http.MethodPut:
			return h.putACL(req)
		}
		return s3err.MethodNotAllowed
	}
	if hasAny(q, "tagging", "retention", "legal-hold", "torrent", "restore", "select", "attributes") {
		return s3err.NotImplemented
	}
	switch r.Method {
	case http.MethodPut:
		if q.Has("uploadId") || q.Has("partNumber") {
			if r.Header.Get("X-Amz-Copy-Source") != "" {
				return h.uploadPartCopy(req)
			}
			return h.uploadPart(req)
		}
		if r.Header.Get("X-Amz-Copy-Source") != "" {
			return h.copyObject(req)
		}
		return h.putObject(req)
	case http.MethodGet, http.MethodHead:
		if q.Has("uploadId") && r.Method == http.MethodGet {
			return h.listParts(req)
		}
		return h.getObject(req)
	case http.MethodDelete:
		if q.Has("uploadId") {
			return h.abortMultipart(req)
		}
		return h.deleteObject(req)
	case http.MethodPost:
		if q.Has("uploads") {
			return h.createMultipart(req)
		}
		if q.Has("uploadId") {
			return h.completeMultipart(req)
		}
		return s3err.NotImplemented
	}
	return s3err.MethodNotAllowed
}

func hasAny(q url.Values, keys ...string) bool {
	for _, k := range keys {
		if q.Has(k) {
			return true
		}
	}
	return false
}

// authorize checks the authenticated key (or anonymous access) against the
// requested action on bucket.
func (h *Handler) authorize(req *request, bucket string, act action) error {
	k := req.auth.Key
	if k == nil {
		// Anonymous: only reading objects from public buckets.
		if act == actRead && req.key != "" && (req.r.Method == http.MethodGet || req.r.Method == http.MethodHead) &&
			bucket == req.bucket && h.Quota.Settings(bucket).PublicRead {
			return nil
		}
		return s3err.AccessDenied
	}
	return allowed(k, bucket, act)
}

// allowed applies a key's bucket list and permission.
func allowed(k *state.AccessKey, bucket string, act action) error {
	switch act {
	case actListBuckets:
		return nil
	case actAdmin:
		if k.AllBuckets() && k.CanWrite() {
			return nil
		}
		return s3err.AccessDenied.WithMessage("This key cannot create or delete buckets. Use a key with access to all buckets, or create the bucket in the admin panel.")
	}
	if !k.CanAccess(bucket) {
		return s3err.AccessDenied.WithMessage("This key does not have access to bucket " + bucket + ".")
	}
	if act == actRead && !k.CanRead() {
		return s3err.AccessDenied.WithMessage("This key is write-only.")
	}
	if act == actWrite && !k.CanWrite() {
		return s3err.AccessDenied.WithMessage("This key is read-only.")
	}
	return nil
}

func (h *Handler) requireBucket(req *request, act action) error {
	if err := h.authorize(req, req.bucket, act); err != nil {
		return err
	}
	if !h.Store.BucketExists(req.bucket) {
		return s3err.NoSuchBucket
	}
	return nil
}

// ---- responses --------------------------------------------------------------

func writeXML(w http.ResponseWriter, status int, v any) {
	b, err := xml.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(xml.Header)+len(b)))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	_, _ = w.Write(b)
}

// ToS3Error maps storage and other errors to S3 errors.
func ToS3Error(err error) *s3err.Error {
	var se *s3err.Error
	if errors.As(err, &se) {
		return se
	}
	switch {
	case errors.Is(err, storage.ErrNoSuchBucket):
		return s3err.NoSuchBucket
	case errors.Is(err, storage.ErrNoSuchKey):
		return s3err.NoSuchKey
	case errors.Is(err, storage.ErrBucketExists):
		return s3err.BucketAlreadyOwnedByYou
	case errors.Is(err, storage.ErrBucketNotEmpty):
		return s3err.BucketNotEmpty
	case errors.Is(err, storage.ErrInvalidBucketName):
		return s3err.InvalidBucketName
	case errors.Is(err, storage.ErrInvalidKey):
		return s3err.InvalidKey
	case errors.Is(err, storage.ErrKeyTooLong):
		return s3err.KeyTooLong
	case errors.Is(err, storage.ErrPathConflict):
		return s3err.PathConflict
	case errors.Is(err, storage.ErrBadDigest):
		return s3err.BadDigest
	case errors.Is(err, storage.ErrIncompleteBody), errors.Is(err, io.ErrUnexpectedEOF):
		return s3err.IncompleteBody
	case errors.Is(err, storage.ErrEntityTooLarge):
		return s3err.EntityTooLarge
	case errors.Is(err, storage.ErrFolderNotEmpty):
		return s3err.InvalidArgument.WithMessage(storage.ErrFolderNotEmpty.Error())
	case errors.Is(err, storage.ErrNoSuchUpload):
		return s3err.NoSuchUpload
	case errors.Is(err, storage.ErrInvalidPart):
		return s3err.InvalidPart
	case errors.Is(err, storage.ErrInvalidPartOrder):
		return s3err.InvalidPartOrder
	case errors.Is(err, storage.ErrEntityTooSmall):
		return s3err.EntityTooSmall
	case errors.Is(err, storage.ErrInvalidPartNum):
		return s3err.InvalidArgument.WithMessage(storage.ErrInvalidPartNum.Error())
	}
	return s3err.InternalError
}

func (h *Handler) fail(req *request, err error) {
	h.Stats.Errors.Add(1)
	e := ToS3Error(err)
	if e == s3err.InternalError {
		h.Log.Error("request failed", "id", req.id, "method", req.r.Method, "path", req.r.URL.Path, "err", err)
	}
	// Drain a little of an unread body so keep-alive connections survive
	// small error responses; large bodies are cut off.
	if req.r.Body != nil {
		_, _ = io.CopyN(io.Discard, req.r.Body, 64<<10)
	}
	if req.r.Method == http.MethodHead {
		req.w.WriteHeader(e.Status)
		return
	}
	if h.ErrorFormat == "json" || (h.ErrorFormat != "xml" && WantsJSON(req.r.Header.Get("Accept"))) {
		writeJSONError(req.w, e, req)
		return
	}
	writeXML(req.w, e.Status, errorResponse{
		Code: e.Code, Message: e.Message, BucketName: req.bucket, Key: req.key,
		Resource: req.r.URL.Path, RequestID: req.id,
	})
}

// ---- buckets ----------------------------------------------------------------

func (h *Handler) listBuckets(req *request) error {
	if err := h.authorize(req, "", actListBuckets); err != nil {
		return err
	}
	buckets, err := h.Store.ListBuckets()
	if err != nil {
		return err
	}
	out := listAllMyBucketsResult{Xmlns: s3NS, Owner: tijoriOwner, Buckets: []bucketItem{}}
	for _, b := range buckets {
		if req.auth.Key.CanAccess(b.Name) {
			out.Buckets = append(out.Buckets, bucketItem{Name: b.Name, CreationDate: s3Time(b.Created)})
		}
	}
	writeXML(req.w, http.StatusOK, out)
	return nil
}

func (h *Handler) createBucket(req *request) error {
	if err := h.authorize(req, req.bucket, actAdmin); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(req.r.Body, 64<<10))
	if err := h.Store.CreateBucket(req.bucket); err != nil {
		return err
	}
	h.Quota.Track(req.bucket)
	h.Log.Info("bucket created", "bucket", req.bucket)
	req.w.Header().Set("Location", "/"+req.bucket)
	req.w.WriteHeader(http.StatusOK)
	return nil
}

func (h *Handler) deleteBucket(req *request) error {
	if err := h.authorize(req, req.bucket, actAdmin); err != nil {
		return err
	}
	if err := h.Store.DeleteBucket(req.bucket, false); err != nil {
		return err
	}
	h.Quota.Forget(req.bucket)
	h.Log.Info("bucket deleted", "bucket", req.bucket)
	req.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) headBucket(req *request) error {
	err := h.authorize(req, req.bucket, actRead)
	// HeadBucket must work for write-only keys too.
	if err != nil && req.auth.Key != nil && allowed(req.auth.Key, req.bucket, actWrite) == nil {
		err = nil
	}
	if err != nil {
		return err
	}
	if !h.Store.BucketExists(req.bucket) {
		return s3err.NoSuchBucket
	}
	req.w.Header().Set("x-amz-bucket-region", h.Region)
	req.w.WriteHeader(http.StatusOK)
	return nil
}

func (h *Handler) getBucketLocation(req *request) error {
	if err := h.requireBucket(req, actRead); err != nil {
		return err
	}
	writeXML(req.w, http.StatusOK, locationConstraint{Xmlns: s3NS})
	return nil
}

func (h *Handler) getBucketVersioning(req *request) error {
	if err := h.requireBucket(req, actRead); err != nil {
		return err
	}
	writeXML(req.w, http.StatusOK, versioningConfiguration{Xmlns: s3NS})
	return nil
}

// ---- listing ----------------------------------------------------------------

func parseMaxKeys(q url.Values, def int) (int, error) {
	v := q.Get("max-keys")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, s3err.InvalidArgument.WithMessage("max-keys must be a non-negative integer.")
	}
	if n > 1000 {
		n = 1000
	}
	return n, nil
}

func toListObjects(objs []storage.ObjectInfo, enc func(string) string, withOwner bool) []listObject {
	out := make([]listObject, 0, len(objs))
	for _, o := range objs {
		lo := listObject{Key: enc(o.Key), LastModified: s3Time(o.LastModified), ETag: `"` + o.ETag + `"`, Size: o.Size, StorageClass: "STANDARD"}
		if withOwner {
			ow := tijoriOwner
			lo.Owner = &ow
		}
		out = append(out, lo)
	}
	return out
}

func encoder(q url.Values) (func(string) string, string, error) {
	switch q.Get("encoding-type") {
	case "":
		return func(s string) string { return s }, "", nil
	case "url":
		return s3URLEncode, "url", nil
	}
	return nil, "", s3err.InvalidArgument.WithMessage("Invalid Encoding Method specified in Request.")
}

func (h *Handler) listObjectsV2(req *request) error {
	if err := h.requireBucket(req, actRead); err != nil {
		return err
	}
	q := req.q
	maxKeys, err := parseMaxKeys(q, 1000)
	if err != nil {
		return err
	}
	enc, encType, err := encoder(q)
	if err != nil {
		return err
	}
	marker := q.Get("start-after")
	token := q.Get("continuation-token")
	if token != "" {
		b, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return s3err.InvalidArgument.WithMessage("The continuation token provided is incorrect.")
		}
		marker = string(b)
	}
	res, err := h.Store.ListObjects(req.bucket, storage.ListOptions{
		Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), Marker: marker, MaxKeys: maxKeys, FetchMeta: true,
	})
	if err != nil {
		return err
	}
	out := listBucketResultV2{
		Xmlns: s3NS, Name: req.bucket, Prefix: enc(q.Get("prefix")), StartAfter: enc(q.Get("start-after")),
		ContinuationToken: token, MaxKeys: maxKeys, Delimiter: enc(q.Get("delimiter")), EncodingType: encType,
		IsTruncated: res.IsTruncated, KeyCount: len(res.Objects) + len(res.CommonPrefixes),
		Contents: toListObjects(res.Objects, enc, q.Get("fetch-owner") == "true"),
	}
	for _, p := range res.CommonPrefixes {
		out.CommonPrefixes = append(out.CommonPrefixes, commonPrefix{enc(p)})
	}
	if res.IsTruncated {
		out.NextContinuationToken = base64.RawURLEncoding.EncodeToString([]byte(res.NextMarker))
	}
	writeXML(req.w, http.StatusOK, out)
	return nil
}

func (h *Handler) listObjectsV1(req *request) error {
	if err := h.requireBucket(req, actRead); err != nil {
		return err
	}
	q := req.q
	maxKeys, err := parseMaxKeys(q, 1000)
	if err != nil {
		return err
	}
	enc, encType, err := encoder(q)
	if err != nil {
		return err
	}
	res, err := h.Store.ListObjects(req.bucket, storage.ListOptions{
		Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), Marker: q.Get("marker"), MaxKeys: maxKeys, FetchMeta: true,
	})
	if err != nil {
		return err
	}
	out := listBucketResultV1{
		Xmlns: s3NS, Name: req.bucket, Prefix: enc(q.Get("prefix")), Marker: enc(q.Get("marker")), MaxKeys: maxKeys,
		Delimiter: enc(q.Get("delimiter")), EncodingType: encType, IsTruncated: res.IsTruncated,
		Contents: toListObjects(res.Objects, enc, true),
	}
	for _, p := range res.CommonPrefixes {
		out.CommonPrefixes = append(out.CommonPrefixes, commonPrefix{enc(p)})
	}
	if res.IsTruncated {
		out.NextMarker = enc(res.NextMarker)
	}
	writeXML(req.w, http.StatusOK, out)
	return nil
}

// ---- objects ----------------------------------------------------------------

// putOptions reads object metadata from request headers.
func putOptions(r *http.Request) storage.PutOptions {
	opt := storage.PutOptions{
		ContentType:        r.Header.Get("Content-Type"),
		CacheControl:       r.Header.Get("Cache-Control"),
		ContentDisposition: r.Header.Get("Content-Disposition"),
		ContentLanguage:    r.Header.Get("Content-Language"),
		Expires:            r.Header.Get("Expires"),
		ContentMD5:         r.Header.Get("Content-Md5"),
		Size:               -1,
	}
	var enc []string
	for _, e := range strings.Split(r.Header.Get("Content-Encoding"), ",") {
		if e = strings.TrimSpace(e); e != "" && !strings.EqualFold(e, "aws-chunked") {
			enc = append(enc, e)
		}
	}
	opt.ContentEncoding = strings.Join(enc, ",")
	for name, vals := range r.Header {
		if k, ok := strings.CutPrefix(strings.ToLower(name), "x-amz-meta-"); ok && len(vals) > 0 {
			if opt.UserMeta == nil {
				opt.UserMeta = map[string]string{}
			}
			opt.UserMeta[k] = strings.Join(vals, ",")
		}
	}
	return opt
}

func (h *Handler) existingSize(bucket, key string) int64 {
	if info, err := h.Store.StatObject(bucket, key); err == nil {
		return info.Size
	}
	return 0
}

// Put stores an object with quota checks, usage accounting and events. It
// is used by the admin panel.
func (h *Handler) Put(bucket, key string, body io.Reader, size int64, opt storage.PutOptions) (storage.PutResult, error) {
	if size < 0 {
		return storage.PutResult{}, s3err.MissingContentLength
	}
	release, err := h.Quota.Reserve(bucket, size, h.existingSize(bucket, key))
	if err != nil {
		return storage.PutResult{}, err
	}
	defer release()
	opt.Size = size
	opt.MaxSize = h.Quota.MaxObjectSize(bucket)
	res, err := h.Store.PutObject(bucket, key, body, opt)
	if err != nil {
		return res, err
	}
	h.recordPut(bucket, res)
	h.Stats.Uploads.Add(1)
	h.Stats.BytesIn.Add(res.Info.Size)
	if h.Events != nil {
		h.Events.ObjectCreated(bucket, key, res.Info)
	}
	return res, nil
}

// Delete removes an object and updates usage. It is used by the admin panel.
func (h *Handler) Delete(bucket, key string) (bool, error) {
	size, existed, err := h.Store.DeleteObject(bucket, key)
	if err == nil && existed {
		h.Quota.Add(bucket, -size, -1)
	}
	return existed, err
}

func (h *Handler) putObject(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	res, err := h.Put(req.bucket, req.key, req.r.Body, req.auth.Size, putOptions(req.r))
	if err != nil {
		return err
	}
	req.w.Header().Set("ETag", `"`+res.Info.ETag+`"`)
	req.w.WriteHeader(http.StatusOK)
	return nil
}

func (h *Handler) recordPut(bucket string, res storage.PutResult) {
	if res.Replaced {
		h.Quota.Add(bucket, res.Info.Size-res.OldSize, 0)
	} else {
		h.Quota.Add(bucket, res.Info.Size, 1)
	}
}

// objectHeaders writes the metadata headers shared by GET and HEAD.
func objectHeaders(w http.ResponseWriter, info storage.ObjectInfo, q url.Values) {
	hd := w.Header()
	defer func() { ProtectActiveContent(hd) }()
	hd.Set("ETag", `"`+info.ETag+`"`)
	hd.Set("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
	hd.Set("Content-Type", info.ContentType)
	hd.Set("Accept-Ranges", "bytes")
	set := func(name, v string) {
		if v != "" {
			hd.Set(name, v)
		}
	}
	set("Cache-Control", info.CacheControl)
	set("Content-Disposition", info.ContentDisposition)
	set("Content-Encoding", info.ContentEncoding)
	set("Content-Language", info.ContentLanguage)
	set("Expires", info.Expires)
	for k, v := range info.UserMeta {
		hd.Set("X-Amz-Meta-"+k, v)
	}
	if info.Parts > 0 {
		hd.Set("x-amz-mp-parts-count", strconv.Itoa(info.Parts))
	}
	// Presigned GETs may override response headers.
	for param, header := range map[string]string{
		"response-content-type":        "Content-Type",
		"response-content-disposition": "Content-Disposition",
		"response-cache-control":       "Cache-Control",
		"response-content-encoding":    "Content-Encoding",
		"response-content-language":    "Content-Language",
		"response-expires":             "Expires",
	} {
		if v := q.Get(param); v != "" {
			hd.Set(header, v)
		}
	}
}

// ProtectActiveContent stops stored HTML, SVG or XML from running scripts
// on Tijori's origin (where the admin panel may live): such responses get a
// sandbox CSP, which gives them an opaque origin. All responses get nosniff.
func ProtectActiveContent(hd http.Header) {
	hd.Set("X-Content-Type-Options", "nosniff")
	ct := strings.ToLower(hd.Get("Content-Type"))
	for _, active := range []string{"html", "svg", "xml", "javascript", "xsl"} {
		if strings.Contains(ct, active) {
			hd.Set("Content-Security-Policy", "sandbox")
			return
		}
	}
}

// checkConditions evaluates If-* headers. It returns (304/412 status, nil)
// to stop, or (0, nil) to continue.
func checkConditions(r *http.Request, etag string, mod time.Time) (int, error) {
	mod = mod.Truncate(time.Second)
	match := func(header string) bool {
		for _, t := range strings.Split(header, ",") {
			t = strings.TrimSpace(t)
			if t == "*" || strings.Trim(strings.TrimPrefix(t, "W/"), `"`) == etag {
				return true
			}
		}
		return false
	}
	if v := r.Header.Get("If-Match"); v != "" && !match(v) {
		return 0, s3err.PreconditionFailed
	}
	if v := r.Header.Get("If-Unmodified-Since"); v != "" && r.Header.Get("If-Match") == "" {
		if t, err := http.ParseTime(v); err == nil && mod.After(t) {
			return 0, s3err.PreconditionFailed
		}
	}
	if v := r.Header.Get("If-None-Match"); v != "" {
		if match(v) {
			return http.StatusNotModified, nil
		}
	} else if v := r.Header.Get("If-Modified-Since"); v != "" {
		if t, err := http.ParseTime(v); err == nil && !mod.After(t) {
			return http.StatusNotModified, nil
		}
	}
	return 0, nil
}

// parseRange parses a single "bytes=" range. ok=false means "ignore the
// header and send everything"; an error means the range is unsatisfiable.
func parseRange(h string, size int64) (start, length int64, ok bool, err error) {
	spec, found := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false, nil
	}
	a, b, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false, nil
	}
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" {
		n, perr := strconv.ParseInt(b, 10, 64)
		if perr != nil || n < 0 {
			return 0, 0, false, nil
		}
		if n == 0 {
			return 0, 0, false, s3err.InvalidRange
		}
		if n > size {
			n = size
		}
		return size - n, n, true, nil
	}
	s, perr := strconv.ParseInt(a, 10, 64)
	if perr != nil || s < 0 {
		return 0, 0, false, nil
	}
	if s >= size {
		return 0, 0, false, s3err.InvalidRange
	}
	e := size - 1
	if b != "" {
		e, perr = strconv.ParseInt(b, 10, 64)
		if perr != nil || e < s {
			return 0, 0, false, nil
		}
		if e >= size {
			e = size - 1
		}
	}
	return s, e - s + 1, true, nil
}

func (h *Handler) getObject(req *request) error {
	if err := h.requireBucket(req, actRead); err != nil {
		return err
	}
	if pn := req.q.Get("partNumber"); pn != "" && pn != "1" {
		return s3err.NotImplemented.WithMessage("GetObject with partNumber is not supported; use a Range header.")
	}
	w, r := req.w, req.r
	if r.Method == http.MethodHead {
		info, err := h.Store.StatObject(req.bucket, req.key)
		if err != nil {
			return err
		}
		objectHeaders(w, info, req.q)
		if st, err := checkConditions(r, info.ETag, info.LastModified); err != nil || st != 0 {
			if err != nil {
				return err
			}
			w.WriteHeader(st)
			return nil
		}
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		w.WriteHeader(http.StatusOK)
		return nil
	}
	f, info, err := h.Store.GetObject(req.bucket, req.key)
	if err != nil {
		return err
	}
	defer f.Close()
	objectHeaders(w, info, req.q)
	if st, err := checkConditions(r, info.ETag, info.LastModified); err != nil || st != 0 {
		if err != nil {
			return err
		}
		w.WriteHeader(st)
		return nil
	}
	start, length, status := int64(0), info.Size, http.StatusOK
	if rh := r.Header.Get("Range"); rh != "" {
		s, n, ok, err := parseRange(rh, info.Size)
		if err != nil {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", info.Size))
			return err
		}
		if ok {
			start, length, status = s, n, http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, s+n-1, info.Size))
		}
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(status)
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil
		}
	}
	n, _ := io.CopyN(w, f, length)
	h.Stats.Downloads.Add(1)
	h.Stats.BytesOut.Add(n)
	return nil
}

func (h *Handler) deleteObject(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	if _, err := h.Delete(req.bucket, req.key); err != nil {
		return err
	}
	req.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) deleteObjects(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	var in deleteRequest
	if err := xml.NewDecoder(io.LimitReader(req.r.Body, 4<<20)).Decode(&in); err != nil {
		return s3err.MalformedXML
	}
	if len(in.Objects) > 1000 {
		return s3err.MalformedXML.WithMessage("You can delete at most 1000 objects per request.")
	}
	out := deleteResult{Xmlns: s3NS}
	for _, o := range in.Objects {
		if _, err := h.Delete(req.bucket, o.Key); err != nil {
			e := ToS3Error(err)
			out.Errors = append(out.Errors, deleteError{Key: o.Key, Code: e.Code, Message: e.Message})
			continue
		}
		if !in.Quiet {
			out.Deleted = append(out.Deleted, deletedObject{Key: o.Key})
		}
	}
	writeXML(req.w, http.StatusOK, out)
	return nil
}

// parseCopySource decodes x-amz-copy-source into bucket and key.
func parseCopySource(v string) (string, string, error) {
	if i := strings.Index(v, "?"); i >= 0 {
		vq, _ := url.ParseQuery(v[i+1:])
		if id := vq.Get("versionId"); id != "" && id != "null" {
			return "", "", s3err.NotImplemented.WithMessage("Object versions are not supported.")
		}
		v = v[:i]
	}
	dec, err := url.PathUnescape(v)
	if err != nil {
		return "", "", s3err.InvalidArgument.WithMessage("Invalid copy source encoding.")
	}
	b, k := SplitPath("/" + strings.TrimPrefix(dec, "/"))
	if b == "" || k == "" {
		return "", "", s3err.InvalidArgument.WithMessage("Copy source must be bucket/key.")
	}
	return b, k, nil
}

func (h *Handler) checkCopySource(req *request) (string, string, storage.ObjectInfo, error) {
	srcB, srcK, err := parseCopySource(req.r.Header.Get("X-Amz-Copy-Source"))
	if err != nil {
		return "", "", storage.ObjectInfo{}, err
	}
	if err := h.authorize(req, srcB, actRead); err != nil {
		return "", "", storage.ObjectInfo{}, err
	}
	info, err := h.Store.StatObject(srcB, srcK)
	if err != nil {
		return "", "", info, err
	}
	hd := req.r.Header
	if v := hd.Get("X-Amz-Copy-Source-If-Match"); v != "" && strings.Trim(v, `"`) != info.ETag {
		return "", "", info, s3err.PreconditionFailed
	}
	if v := hd.Get("X-Amz-Copy-Source-If-None-Match"); v != "" && strings.Trim(v, `"`) == info.ETag {
		return "", "", info, s3err.PreconditionFailed
	}
	return srcB, srcK, info, nil
}

func (h *Handler) copyObject(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	srcB, srcK, info, err := h.checkCopySource(req)
	if err != nil {
		return err
	}
	replace := strings.EqualFold(req.r.Header.Get("X-Amz-Metadata-Directive"), "REPLACE")
	if srcB == req.bucket && srcK == req.key && !replace {
		return s3err.InvalidRequest.WithMessage("This copy request is illegal because it is trying to copy an object to itself without changing the object's metadata.")
	}
	release, err := h.Quota.Reserve(req.bucket, info.Size, h.existingSize(req.bucket, req.key))
	if err != nil {
		return err
	}
	defer release()
	var opt *storage.PutOptions
	if replace {
		o := putOptions(req.r)
		opt = &o
	}
	res, err := h.Store.CopyObject(srcB, srcK, req.bucket, req.key, opt, h.Quota.MaxObjectSize(req.bucket))
	if err != nil {
		return err
	}
	h.recordPut(req.bucket, res)
	writeXML(req.w, http.StatusOK, copyObjectResult{Xmlns: s3NS, LastModified: s3Time(res.Info.LastModified), ETag: `"` + res.Info.ETag + `"`})
	if h.Events != nil && (srcB != req.bucket || srcK != req.key) {
		h.Events.ObjectCreated(req.bucket, req.key, res.Info)
	}
	return nil
}

// ---- ACLs -------------------------------------------------------------------

// Tijori has no per-object ACLs: visibility is the bucket's public_read
// setting. GetObjectAcl reports it (Flysystem/Laravel read it on copy and
// move) and PutObjectAcl is accepted without changing anything.

const aclPrivate = `<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>tijori</ID><DisplayName>tijori</DisplayName></Owner><AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>tijori</ID><DisplayName>tijori</DisplayName></Grantee><Permission>FULL_CONTROL</Permission></Grant>%s</AccessControlList></AccessControlPolicy>`
const aclPublicGrant = `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee><Permission>READ</Permission></Grant>`

func (h *Handler) getACL(req *request) error {
	if err := h.requireBucket(req, actRead); err != nil {
		return err
	}
	if req.key != "" {
		if _, err := h.Store.StatObject(req.bucket, req.key); err != nil {
			return err
		}
	}
	extra := ""
	if h.Quota.Settings(req.bucket).PublicRead {
		extra = aclPublicGrant
	}
	body := xml.Header + fmt.Sprintf(aclPrivate, extra)
	req.w.Header().Set("Content-Type", "application/xml")
	req.w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	req.w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(req.w, body)
	return nil
}

func (h *Handler) putACL(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	if req.key != "" {
		if _, err := h.Store.StatObject(req.bucket, req.key); err != nil {
			return err
		}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(req.r.Body, 64<<10))
	req.w.WriteHeader(http.StatusOK)
	return nil
}

// ---- multipart --------------------------------------------------------------

func (h *Handler) createMultipart(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	id, err := h.Store.CreateMultipartUpload(req.bucket, req.key, putOptions(req.r))
	if err != nil {
		return err
	}
	writeXML(req.w, http.StatusOK, initiateMultipartUploadResult{Xmlns: s3NS, Bucket: req.bucket, Key: req.key, UploadID: id})
	return nil
}

func partNumber(q url.Values) (int, error) {
	n, err := strconv.Atoi(q.Get("partNumber"))
	if err != nil || n < 1 || n > 10000 {
		return 0, s3err.InvalidArgument.WithMessage("Part number must be an integer between 1 and 10000, inclusive.")
	}
	return n, nil
}

func (h *Handler) uploadPart(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	num, err := partNumber(req.q)
	if err != nil {
		return err
	}
	size := req.auth.Size
	if size < 0 {
		return s3err.MissingContentLength
	}
	id := req.q.Get("uploadId")
	if _, err := h.Store.GetUpload(req.bucket, req.key, id); err != nil {
		return err
	}
	if err := h.Quota.CheckPart(req.bucket, h.Store.PendingBytes(req.bucket, req.key, id)+size); err != nil {
		return err
	}
	p, err := h.Store.UploadPart(req.bucket, req.key, id, num, req.r.Body, size, req.r.Header.Get("Content-Md5"), 0)
	if err != nil {
		return err
	}
	h.Stats.BytesIn.Add(p.Size)
	req.w.Header().Set("ETag", `"`+p.ETag+`"`)
	req.w.WriteHeader(http.StatusOK)
	return nil
}

func (h *Handler) uploadPartCopy(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	num, err := partNumber(req.q)
	if err != nil {
		return err
	}
	id := req.q.Get("uploadId")
	if _, err := h.Store.GetUpload(req.bucket, req.key, id); err != nil {
		return err
	}
	srcB, srcK, info, err := h.checkCopySource(req)
	if err != nil {
		return err
	}
	start, length := int64(0), info.Size
	if rh := req.r.Header.Get("X-Amz-Copy-Source-Range"); rh != "" {
		s, n, ok, err := parseRange(rh, info.Size)
		if err != nil || !ok {
			return s3err.InvalidRange.WithMessage("The x-amz-copy-source-range value must be of the form bytes=first-last and inside the source object.")
		}
		start, length = s, n
	}
	if err := h.Quota.CheckPart(req.bucket, h.Store.PendingBytes(req.bucket, req.key, id)+length); err != nil {
		return err
	}
	f, _, err := h.Store.GetObject(srcB, srcK)
	if err != nil {
		return err
	}
	defer f.Close()
	p, err := h.Store.UploadPart(req.bucket, req.key, id, num, io.NewSectionReader(f, start, length), length, "", 0)
	if err != nil {
		return err
	}
	writeXML(req.w, http.StatusOK, copyPartResult{Xmlns: s3NS, LastModified: s3Time(p.LastModified), ETag: `"` + p.ETag + `"`})
	return nil
}

func (h *Handler) completeMultipart(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	var in completeMultipartUpload
	if err := xml.NewDecoder(io.LimitReader(req.r.Body, 4<<20)).Decode(&in); err != nil {
		return s3err.MalformedXML
	}
	parts := make([]storage.CompletePart, len(in.Parts))
	for i, p := range in.Parts {
		parts[i] = storage.CompletePart{Number: p.PartNumber, ETag: p.ETag}
	}
	var release func()
	check := func(total int64) error {
		var err error
		release, err = h.Quota.Reserve(req.bucket, total, h.existingSize(req.bucket, req.key))
		return err
	}
	res, err := h.Store.CompleteMultipartUpload(req.bucket, req.key, req.q.Get("uploadId"), parts, check)
	if release != nil {
		defer release()
	}
	if err != nil {
		return err
	}
	h.recordPut(req.bucket, res)
	h.Stats.Uploads.Add(1)
	scheme := "http"
	if req.r.TLS != nil || req.r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	loc := scheme + "://" + req.r.Host + "/" + req.bucket + "/" + req.key
	writeXML(req.w, http.StatusOK, completeMultipartUploadResult{Xmlns: s3NS, Location: loc, Bucket: req.bucket, Key: req.key, ETag: `"` + res.Info.ETag + `"`})
	if h.Events != nil {
		h.Events.ObjectCreated(req.bucket, req.key, res.Info)
	}
	return nil
}

func (h *Handler) abortMultipart(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	if err := h.Store.AbortMultipartUpload(req.bucket, req.key, req.q.Get("uploadId")); err != nil {
		return err
	}
	req.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) listParts(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	id := req.q.Get("uploadId")
	parts, err := h.Store.ListParts(req.bucket, req.key, id)
	if err != nil {
		return err
	}
	maxParts := 1000
	if v := req.q.Get("max-parts"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < maxParts {
			maxParts = n
		}
	}
	marker, _ := strconv.Atoi(req.q.Get("part-number-marker"))
	out := listPartsResult{Xmlns: s3NS, Bucket: req.bucket, Key: req.key, UploadID: id, Initiator: tijoriOwner, Owner: tijoriOwner,
		StorageClass: "STANDARD", PartNumberMarker: marker, MaxParts: maxParts}
	for _, p := range parts {
		if p.Number <= marker {
			continue
		}
		if len(out.Parts) >= maxParts {
			out.IsTruncated = true
			break
		}
		out.Parts = append(out.Parts, partItem{PartNumber: p.Number, LastModified: s3Time(p.LastModified), ETag: `"` + p.ETag + `"`, Size: p.Size})
		out.NextPartNumberMarker = p.Number
	}
	writeXML(req.w, http.StatusOK, out)
	return nil
}

func (h *Handler) listMultipartUploads(req *request) error {
	if err := h.requireBucket(req, actWrite); err != nil {
		return err
	}
	ups, err := h.Store.ListUploads(req.bucket, req.q.Get("prefix"))
	if err != nil {
		return err
	}
	out := listMultipartUploadsResult{Xmlns: s3NS, Bucket: req.bucket, Prefix: req.q.Get("prefix"), MaxUploads: 1000}
	for _, u := range ups {
		if len(out.Uploads) >= 1000 {
			out.IsTruncated = true
			break
		}
		out.Uploads = append(out.Uploads, uploadItem{Key: u.Key, UploadID: u.ID, Initiator: tijoriOwner, Owner: tijoriOwner,
			StorageClass: "STANDARD", Initiated: s3Time(u.Initiated)})
		out.NextKeyMarker, out.NextUploadIDMarker = u.Key, u.ID
	}
	writeXML(req.w, http.StatusOK, out)
	return nil
}
