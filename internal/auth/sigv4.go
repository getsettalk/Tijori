// Package auth verifies AWS Signature Version 4 requests (header and
// presigned query-string forms, including aws-chunked streaming payloads)
// and creates presigned URLs.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/getsettalk/Tijori/internal/s3err"
	"github.com/getsettalk/Tijori/internal/state"
)

const (
	algorithm        = "AWS4-HMAC-SHA256"
	amzDateFormat    = "20060102T150405Z"
	shortDateFormat  = "20060102"
	unsignedPayload  = "UNSIGNED-PAYLOAD"
	emptySHA256      = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	streamingSigned  = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamingSignedT = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	streamingUnsignT = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	// MaxSkew is the allowed difference between client and server clocks.
	MaxSkew = 15 * time.Minute
	// MaxPresignExpiry is S3's limit of 7 days.
	MaxPresignExpiry = 7 * 24 * time.Hour
)

// KeyStore looks up access keys.
type KeyStore interface {
	Key(id string) (state.AccessKey, bool)
}

// Verifier checks request signatures.
type Verifier struct {
	Keys KeyStore
	Now  func() time.Time
}

// Result is a successfully authenticated request.
type Result struct {
	// Key is nil for anonymous requests.
	Key *state.AccessKey
	// Body replaces r.Body: it decodes aws-chunked payloads and verifies
	// payload hashes as it is read.
	Body io.ReadCloser
	// Size is the decoded payload length, or -1 if unknown.
	Size int64
	// Presigned is true for query-string authentication.
	Presigned bool
}

type sigParams struct {
	accessKey     string
	date          string // yyyymmdd
	region        string
	service       string
	signedHeaders []string
	signature     string
	amzDate       time.Time
	amzDateRaw    string
}

func (p sigParams) scope() string {
	return p.date + "/" + p.region + "/" + p.service + "/aws4_request"
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify authenticates r. Requests without any credentials are anonymous
// (Result.Key == nil); the caller decides whether anonymous access is allowed.
func (v *Verifier) Verify(r *http.Request) (*Result, error) {
	authz := r.Header.Get("Authorization")
	q := r.URL.Query()
	switch {
	case strings.HasPrefix(authz, algorithm):
		return v.verifyHeader(r, authz)
	case q.Get("X-Amz-Algorithm") != "" || q.Get("X-Amz-Credential") != "":
		return v.verifyPresigned(r, q)
	case authz != "" || q.Get("AWSAccessKeyId") != "":
		return nil, s3err.SignatureVersionNotSupported
	}
	return &Result{Body: r.Body, Size: r.ContentLength}, nil
}

func parseCredential(cred string) (sigParams, bool) {
	var p sigParams
	parts := strings.Split(cred, "/")
	if len(parts) != 5 || parts[4] != "aws4_request" || len(parts[1]) != 8 {
		return p, false
	}
	p.accessKey, p.date, p.region, p.service = parts[0], parts[1], parts[2], parts[3]
	return p, p.accessKey != ""
}

func parseAuthHeader(h string) (sigParams, error) {
	var p sigParams
	rest := strings.TrimSpace(strings.TrimPrefix(h, algorithm))
	fields := map[string]string{}
	for _, f := range strings.Split(rest, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(f), "=")
		if !ok {
			return p, s3err.AuthorizationHeaderMalformed
		}
		fields[k] = strings.TrimSpace(val)
	}
	p, ok := parseCredential(fields["Credential"])
	if !ok || fields["SignedHeaders"] == "" || fields["Signature"] == "" {
		return p, s3err.AuthorizationHeaderMalformed
	}
	p.signedHeaders = strings.Split(fields["SignedHeaders"], ";")
	p.signature = fields["Signature"]
	return p, nil
}

func (v *Verifier) lookup(p sigParams) (state.AccessKey, error) {
	if p.service != "s3" {
		return state.AccessKey{}, s3err.AuthorizationHeaderMalformed.WithMessage("The service in the credential scope must be 's3'.")
	}
	k, ok := v.Keys.Key(p.accessKey)
	if !ok {
		return k, s3err.InvalidAccessKeyID
	}
	return k, nil
}

func (v *Verifier) verifyHeader(r *http.Request, authz string) (*Result, error) {
	p, err := parseAuthHeader(authz)
	if err != nil {
		return nil, err
	}
	p.amzDateRaw = r.Header.Get("X-Amz-Date")
	if p.amzDateRaw != "" {
		p.amzDate, err = time.Parse(amzDateFormat, p.amzDateRaw)
	} else if d := r.Header.Get("Date"); d != "" {
		p.amzDate, err = http.ParseTime(d)
		p.amzDateRaw = p.amzDate.UTC().Format(amzDateFormat)
	} else {
		return nil, s3err.AccessDenied.WithMessage("AWS authentication requires a valid Date or x-amz-date header.")
	}
	if err != nil {
		return nil, s3err.AccessDenied.WithMessage("AWS authentication requires a valid Date or x-amz-date header.")
	}
	if !strings.HasPrefix(p.amzDateRaw, p.date) {
		return nil, s3err.AuthorizationHeaderMalformed.WithMessage("The credential date does not match the request date.")
	}
	if d := v.now().Sub(p.amzDate); d > MaxSkew || d < -MaxSkew {
		return nil, s3err.RequestTimeTooSkewed
	}
	key, err := v.lookup(p)
	if err != nil {
		return nil, err
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
			return nil, s3err.MissingContentSHA256
		}
		payload = emptySHA256
	}
	signingKey := SigningKey(key.SecretKey, p.date, p.region, p.service)
	want := signature(signingKey, stringToSign(p, canonicalRequest(r, r.URL.Query(), p.signedHeaders, payload, false)))
	if !hmac.Equal([]byte(want), []byte(p.signature)) {
		return nil, s3err.SignatureDoesNotMatch
	}
	res := &Result{Key: &key, Body: r.Body, Size: r.ContentLength}
	switch payload {
	case unsignedPayload:
	case streamingSigned, streamingSignedT, streamingUnsignT:
		n, err := strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64)
		if err != nil || n < 0 {
			return nil, s3err.MissingContentLength.WithMessage("You must provide the x-amz-decoded-content-length header for aws-chunked uploads.")
		}
		res.Size = n
		res.Body = newChunkedReader(r.Body, chunkConfig{
			signed:     payload != streamingUnsignT,
			trailer:    payload != streamingSigned,
			signingKey: signingKey,
			seed:       p.signature,
			amzDate:    p.amzDateRaw,
			scope:      p.scope(),
			trailerHdr: strings.ToLower(r.Header.Get("X-Amz-Trailer")),
		})
	default:
		if len(payload) != 64 {
			if strings.HasPrefix(payload, "STREAMING-") {
				return nil, s3err.NotImplemented.WithMessage("Payload signing method " + payload + " is not supported.")
			}
			return nil, s3err.InvalidArgument.WithMessage("x-amz-content-sha256 must be UNSIGNED-PAYLOAD, a STREAMING-* value or a SHA256 hex digest.")
		}
		if payload != emptySHA256 || r.ContentLength != 0 {
			res.Body = &hashReader{r: r.Body, h: sha256.New(), want: strings.ToLower(payload)}
		}
	}
	return res, nil
}

func (v *Verifier) verifyPresigned(r *http.Request, q url.Values) (*Result, error) {
	if q.Get("X-Amz-Algorithm") != algorithm {
		return nil, s3err.AuthorizationQueryMalformed.WithMessage("X-Amz-Algorithm only supports \"AWS4-HMAC-SHA256\".")
	}
	p, ok := parseCredential(q.Get("X-Amz-Credential"))
	if !ok {
		return nil, s3err.AuthorizationQueryMalformed
	}
	p.signature = q.Get("X-Amz-Signature")
	p.signedHeaders = strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
	p.amzDateRaw = q.Get("X-Amz-Date")
	var err error
	p.amzDate, err = time.Parse(amzDateFormat, p.amzDateRaw)
	if err != nil || p.signature == "" || q.Get("X-Amz-SignedHeaders") == "" {
		return nil, s3err.AuthorizationQueryMalformed.WithMessage("Query-string authentication requires X-Amz-Date, X-Amz-SignedHeaders and X-Amz-Signature.")
	}
	if !strings.HasPrefix(p.amzDateRaw, p.date) {
		return nil, s3err.AuthorizationQueryMalformed.WithMessage("The credential date does not match X-Amz-Date.")
	}
	exp, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil || exp < 1 || time.Duration(exp)*time.Second > MaxPresignExpiry {
		return nil, s3err.AuthorizationQueryMalformed.WithMessage("X-Amz-Expires must be between 1 and 604800 seconds.")
	}
	now := v.now()
	if p.amzDate.Sub(now) > MaxSkew {
		return nil, s3err.RequestTimeTooSkewed
	}
	if now.After(p.amzDate.Add(time.Duration(exp) * time.Second)) {
		return nil, s3err.ExpiredPresign
	}
	key, err := v.lookup(p)
	if err != nil {
		return nil, err
	}
	payload := q.Get("X-Amz-Content-Sha256")
	if payload == "" {
		payload = unsignedPayload
	}
	signingKey := SigningKey(key.SecretKey, p.date, p.region, p.service)
	want := signature(signingKey, stringToSign(p, canonicalRequest(r, q, p.signedHeaders, payload, true)))
	if !hmac.Equal([]byte(want), []byte(p.signature)) {
		return nil, s3err.SignatureDoesNotMatch
	}
	return &Result{Key: &key, Body: r.Body, Size: r.ContentLength, Presigned: true}, nil
}

// ---- canonicalisation ------------------------------------------------------

func canonicalRequest(r *http.Request, q url.Values, signed []string, payload string, presigned bool) string {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte('\n')
	b.WriteString(canonicalURI(r.URL.Path))
	b.WriteByte('\n')
	b.WriteString(canonicalQuery(q, presigned))
	b.WriteByte('\n')
	for _, h := range signed {
		b.WriteString(h)
		b.WriteByte(':')
		b.WriteString(headerValue(r, h))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(signed, ";"))
	b.WriteByte('\n')
	b.WriteString(payload)
	return b.String()
}

func headerValue(r *http.Request, name string) string {
	switch name {
	case "host":
		return r.Host
	case "content-length":
		if v := r.Header.Get("Content-Length"); v != "" {
			return v
		}
		return strconv.FormatInt(r.ContentLength, 10)
	case "transfer-encoding":
		return strings.Join(r.TransferEncoding, ",")
	}
	vals := r.Header.Values(name)
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(out, ",")
}

func canonicalQuery(q url.Values, presigned bool) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		if presigned && k == "X-Amz-Signature" {
			continue
		}
		keys = append(keys, k)
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, k := range keys {
		for _, v := range q[k] {
			pairs = append(pairs, kv{uriEncode(k, true), uriEncode(v, true)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	return uriEncode(path, false)
}

// uriEncode implements AWS's UriEncode: everything except unreserved
// characters is percent-encoded; "/" is kept unless encodeSlash is set.
func uriEncode(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || (c == '/' && !encodeSlash) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

func stringToSign(p sigParams, canonical string) string {
	h := sha256.Sum256([]byte(canonical))
	return algorithm + "\n" + p.amzDateRaw + "\n" + p.scope() + "\n" + hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// SigningKey derives the SigV4 signing key.
func SigningKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, "aws4_request")
}

func signature(signingKey []byte, sts string) string {
	return hex.EncodeToString(hmacSHA256(signingKey, sts))
}

// hashReader verifies the SHA256 of a body once it has been fully read.
type hashReader struct {
	r    io.ReadCloser
	h    hash.Hash
	want string
	done bool
}

func (h *hashReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	if err == io.EOF && !h.done {
		h.done = true
		if hex.EncodeToString(h.h.Sum(nil)) != h.want {
			return n, s3err.ContentSHA256Mismatch
		}
	}
	return n, err
}

func (h *hashReader) Close() error { return h.r.Close() }

// ---- presigning -------------------------------------------------------------

// PresignOptions describes a URL to presign.
type PresignOptions struct {
	Method    string
	URL       string // full URL, for example https://files.example.com/bucket/key
	AccessKey string
	SecretKey string
	Region    string
	Expires   time.Duration
	Now       time.Time
}

// Presign returns a presigned URL valid for opt.Expires.
func Presign(opt PresignOptions) (string, error) {
	u, err := url.Parse(opt.URL)
	if err != nil {
		return "", err
	}
	if opt.Region == "" {
		opt.Region = "auto"
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	if opt.Expires <= 0 || opt.Expires > MaxPresignExpiry {
		opt.Expires = MaxPresignExpiry
	}
	now := opt.Now.UTC()
	p := sigParams{
		accessKey:     opt.AccessKey,
		date:          now.Format(shortDateFormat),
		region:        opt.Region,
		service:       "s3",
		signedHeaders: []string{"host"},
		amzDateRaw:    now.Format(amzDateFormat),
	}
	q := u.Query()
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", opt.AccessKey+"/"+p.scope())
	q.Set("X-Amz-Date", p.amzDateRaw)
	q.Set("X-Amz-Expires", strconv.Itoa(int(opt.Expires/time.Second)))
	q.Set("X-Amz-SignedHeaders", "host")
	req := &http.Request{Method: opt.Method, URL: u, Host: u.Host, Header: http.Header{}}
	sk := SigningKey(opt.SecretKey, p.date, p.region, p.service)
	sig := signature(sk, stringToSign(p, canonicalRequest(req, q, p.signedHeaders, unsignedPayload, true)))
	u.RawQuery = canonicalQuery(q, true) + "&X-Amz-Signature=" + sig
	return u.String(), nil
}

// SignRequest adds SigV4 header authentication to an outgoing request. The
// payload is sent as UNSIGNED-PAYLOAD. Used by the s3:// backup target.
func SignRequest(req *http.Request, accessKey, secretKey, region string, now time.Time) {
	if region == "" {
		region = "auto"
	}
	now = now.UTC()
	p := sigParams{
		accessKey:  accessKey,
		date:       now.Format(shortDateFormat),
		region:     region,
		service:    "s3",
		amzDateRaw: now.Format(amzDateFormat),
	}
	req.Header.Set("X-Amz-Date", p.amzDateRaw)
	req.Header.Set("X-Amz-Content-Sha256", unsignedPayload)
	if req.Host == "" {
		req.Host = req.URL.Host
	}
	p.signedHeaders = []string{"host", "x-amz-content-sha256", "x-amz-date"}
	sk := SigningKey(secretKey, p.date, p.region, p.service)
	sig := signature(sk, stringToSign(p, canonicalRequest(req, req.URL.Query(), p.signedHeaders, unsignedPayload, false)))
	req.Header.Set("Authorization", algorithm+" Credential="+accessKey+"/"+p.scope()+", SignedHeaders="+strings.Join(p.signedHeaders, ";")+", Signature="+sig)
}
