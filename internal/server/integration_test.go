package server

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/getsettalk/Tijori/internal/auth"
	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/state"
)

type testEnv struct {
	srv *Server
	url string
	key state.AccessKey
	s3  *s3.Client
}

func startServer(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := auth.NewKey("test", []string{"*"}, state.PermReadWrite)
	if err := srv.DB.AddKey(key); err != nil {
		t.Fatal(err)
	}
	srv.Start()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })
	return &testEnv{srv: srv, url: ts.URL, key: key, s3: newClient(ts.URL, key.AccessKey, key.SecretKey)}
}

func newClient(url, ak, sk string) *s3.Client {
	return s3.New(s3.Options{
		Region:           "auto",
		BaseEndpoint:     aws.String(url),
		UsePathStyle:     true,
		Credentials:      credentials.NewStaticCredentialsProvider(ak, sk, ""),
		RetryMaxAttempts: 1,
	})
}

func (e *testEnv) addKey(t *testing.T, buckets []string, perm string) *s3.Client {
	k, _ := auth.NewKey("extra", buckets, perm)
	if err := e.srv.DB.AddKey(k); err != nil {
		t.Fatal(err)
	}
	return newClient(e.url, k.AccessKey, k.SecretKey)
}

func errCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	if err == nil {
		return ""
	}
	return "non-API error: " + err.Error()
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

var ctx = context.Background()

func TestSDKObjectLifecycle(t *testing.T) {
	e := startServer(t, nil)
	c := e.s3
	if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("photos")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("photos")}); errCode(err) != "BucketAlreadyOwnedByYou" {
		t.Fatalf("duplicate bucket: %v", err)
	}
	if _, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String("photos")}); err != nil {
		t.Fatal(err)
	}
	lb, err := c.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil || len(lb.Buckets) != 1 || *lb.Buckets[0].Name != "photos" {
		t.Fatalf("list buckets: %v %+v", err, lb)
	}

	body := randomBytes(100_000)
	put, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("photos"), Key: aws.String("2024/trip/beach photo.jpg"),
		Body: bytes.NewReader(body), ContentType: aws.String("image/jpeg"),
		Metadata: map[string]string{"owner": "asha"}, CacheControl: aws.String("max-age=60"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(body)
	if want := fmt.Sprintf(`"%x"`, sum); aws.ToString(put.ETag) != want {
		t.Fatalf("etag %s want %s", aws.ToString(put.ETag), want)
	}

	head, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("photos"), Key: aws.String("2024/trip/beach photo.jpg")})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToInt64(head.ContentLength) != int64(len(body)) || aws.ToString(head.ContentType) != "image/jpeg" ||
		head.Metadata["owner"] != "asha" || aws.ToString(head.CacheControl) != "max-age=60" {
		t.Fatalf("head: %+v", head)
	}

	get, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("2024/trip/beach photo.jpg"), Range: aws.String("bytes=100-199")})
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if !bytes.Equal(part, body[100:200]) || aws.ToString(get.ContentRange) != fmt.Sprintf("bytes 100-199/%d", len(body)) {
		t.Fatalf("range read wrong: %d bytes, %s", len(part), aws.ToString(get.ContentRange))
	}

	if _, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("missing")}); err == nil {
		t.Fatal("expected error")
	} else {
		var nsk *types.NoSuchKey
		if !errors.As(err, &nsk) {
			t.Fatalf("expected NoSuchKey, got %v", err)
		}
	}

	// Copy, with and without new metadata.
	if _, err := c.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String("photos"), Key: aws.String("copy.jpg"),
		CopySource: aws.String("photos/2024/trip/beach%20photo.jpg")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String("photos"), Key: aws.String("copy.jpg"),
		CopySource: aws.String("photos/copy.jpg"), MetadataDirective: types.MetadataDirectiveReplace,
		ContentType: aws.String("image/x-test")}); err != nil {
		t.Fatal(err)
	}
	head, err = c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("photos"), Key: aws.String("copy.jpg")})
	if err != nil || aws.ToString(head.ContentType) != "image/x-test" || aws.ToInt64(head.ContentLength) != int64(len(body)) {
		t.Fatalf("copy head %v %+v", err, head)
	}

	// Listing with delimiter and pagination.
	for i := 0; i < 5; i++ {
		if _, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("photos"), Key: aws.String(fmt.Sprintf("list/item-%d.txt", i)), Body: strings.NewReader("x")}); err != nil {
			t.Fatal(err)
		}
	}
	top, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("photos"), Delimiter: aws.String("/")})
	if err != nil {
		t.Fatal(err)
	}
	if len(top.Contents) != 1 || len(top.CommonPrefixes) != 2 {
		t.Fatalf("top level: %d objects, %d prefixes", len(top.Contents), len(top.CommonPrefixes))
	}
	pager := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{Bucket: aws.String("photos"), Prefix: aws.String("list/"), MaxKeys: aws.Int32(2)})
	n, pages := 0, 0
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		n += len(page.Contents)
		pages++
	}
	if n != 5 || pages != 3 {
		t.Fatalf("pagination: %d objects in %d pages", n, pages)
	}

	// Delete one, then many.
	if _, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("photos"), Key: aws.String("copy.jpg")}); err != nil {
		t.Fatal(err)
	}
	var ids []types.ObjectIdentifier
	for i := 0; i < 5; i++ {
		ids = append(ids, types.ObjectIdentifier{Key: aws.String(fmt.Sprintf("list/item-%d.txt", i))})
	}
	del, err := c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String("photos"), Delete: &types.Delete{Objects: ids}})
	if err != nil || len(del.Deleted) != 5 || len(del.Errors) != 0 {
		t.Fatalf("delete objects: %v %+v", err, del)
	}
	if u := e.srv.Quota.BucketUsage("photos"); u.Objects != 1 || u.Bytes != int64(len(body)) {
		t.Fatalf("usage after deletes: %+v", u)
	}
	if _, err := c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String("photos")}); errCode(err) != "BucketNotEmpty" {
		t.Fatalf("delete non-empty bucket: %v", err)
	}
}

func TestSDKMultipart(t *testing.T) {
	e := startServer(t, nil)
	c := e.s3
	if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("big")}); err != nil {
		t.Fatal(err)
	}
	body := randomBytes(12 << 20)
	up := transfermanager.New(c, func(o *transfermanager.Options) {
		o.PartSizeBytes = 5 << 20
		o.MultipartUploadThreshold = 5 << 20
		o.Concurrency = 3
	})
	out, err := up.UploadObject(ctx, &transfermanager.UploadObjectInput{Bucket: aws.String("big"), Key: aws.String("video.mp4"), Body: bytes.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.Trim(aws.ToString(out.ETag), `"`), "-3") {
		t.Fatalf("multipart etag %s", aws.ToString(out.ETag))
	}
	get, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("big"), Key: aws.String("video.mp4")})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if md5.Sum(got) != md5.Sum(body) {
		t.Fatal("downloaded data differs")
	}

	// Abort flow.
	cm, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("big"), Key: aws.String("aborted")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String("big"), Key: aws.String("aborted"), UploadId: cm.UploadId,
		PartNumber: aws.Int32(1), Body: bytes.NewReader([]byte("part"))}); err != nil {
		t.Fatal(err)
	}
	lp, err := c.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String("big"), Key: aws.String("aborted"), UploadId: cm.UploadId})
	if err != nil || len(lp.Parts) != 1 {
		t.Fatalf("list parts: %v", err)
	}
	lu, err := c.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String("big")})
	if err != nil || len(lu.Uploads) != 1 {
		t.Fatalf("list uploads: %v %+v", err, lu)
	}
	if _, err := c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String("big"), Key: aws.String("aborted"), UploadId: cm.UploadId}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String("big"), Key: aws.String("aborted"), UploadId: cm.UploadId}); errCode(err) != "NoSuchUpload" {
		t.Fatalf("after abort: %v", err)
	}
}

func TestSDKPresigned(t *testing.T) {
	e := startServer(t, nil)
	if _, err := e.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("uploads")}); err != nil {
		t.Fatal(err)
	}
	ps := s3.NewPresignClient(e.s3)
	putReq, err := ps.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("uploads"), Key: aws.String("from-phone.jpg")}, s3.WithPresignExpires(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, putReq.URL, strings.NewReader("phone bytes"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("presigned put: %v %v", err, resp.Status)
	}
	resp.Body.Close()
	getReq, err := ps.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("uploads"), Key: aws.String("from-phone.jpg")})
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(getReq.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "phone bytes" {
		t.Fatalf("presigned get: %d %q", resp.StatusCode, b)
	}
	// Tampering with the URL breaks it.
	resp, _ = http.Get(strings.Replace(getReq.URL, "from-phone", "other", 1))
	if resp.StatusCode != 403 {
		t.Fatalf("tampered URL status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSDKAuthErrors(t *testing.T) {
	e := startServer(t, nil)
	if _, err := e.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("private")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("other")}); err != nil {
		t.Fatal(err)
	}
	bad := newClient(e.url, e.key.AccessKey, "wrong-secret-wrong-secret-wrong-secret!!")
	if _, err := bad.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("private")}); errCode(err) != "SignatureDoesNotMatch" {
		t.Fatalf("wrong secret: %v", err)
	}
	unknown := newClient(e.url, "tj_UNKNOWNUNKNOWN00", "x")
	if _, err := unknown.ListBuckets(ctx, &s3.ListBucketsInput{}); errCode(err) != "InvalidAccessKeyId" {
		t.Fatalf("unknown key: %v", err)
	}
	ro := e.addKey(t, []string{"private"}, state.PermRead)
	if _, err := ro.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("private"), Key: aws.String("x"), Body: strings.NewReader("x")}); errCode(err) != "AccessDenied" {
		t.Fatalf("read-only put: %v", err)
	}
	if _, err := ro.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("private")}); err != nil {
		t.Fatalf("read-only list: %v", err)
	}
	if _, err := ro.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("other")}); errCode(err) != "AccessDenied" {
		t.Fatalf("other bucket: %v", err)
	}
	lb, err := ro.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil || len(lb.Buckets) != 1 {
		t.Fatalf("restricted key sees %d buckets (%v)", len(lb.Buckets), err)
	}
	if _, err := ro.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("newone")}); errCode(err) != "AccessDenied" {
		t.Fatalf("restricted create bucket: %v", err)
	}
}

func TestQuotaAndPublicRead(t *testing.T) {
	e := startServer(t, func(c *config.Config) {
		c.Buckets["small"] = config.BucketConfig{MaxSize: 1024, MaxObjectSize: 800, PublicRead: true}
	})
	c := e.s3
	if _, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("small"), Key: aws.String("a"), Body: bytes.NewReader(make([]byte, 900))}); errCode(err) != "EntityTooLarge" {
		t.Fatalf("object limit: %v", err)
	}
	if _, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("small"), Key: aws.String("a"), Body: bytes.NewReader(make([]byte, 700))}); err != nil {
		t.Fatal(err)
	}
	_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("small"), Key: aws.String("b"), Body: bytes.NewReader(make([]byte, 700))})
	if errCode(err) != "QuotaExceeded" {
		t.Fatalf("quota: %v", err)
	}
	if _, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("small"), Key: aws.String("b")}); err == nil {
		t.Fatal("rejected upload was written")
	}
	// Overwriting within quota is allowed.
	if _, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("small"), Key: aws.String("a"), Body: bytes.NewReader(make([]byte, 600))}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	// Anonymous read works on a public bucket; anonymous write and list do not.
	resp, err := http.Get(e.url + "/small/a")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("anonymous get: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	req, _ := http.NewRequest(http.MethodPut, e.url+"/small/evil", strings.NewReader("x"))
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("anonymous put status %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, _ = http.Get(e.url + "/small?list-type=2")
	if resp.StatusCode != 403 {
		t.Fatalf("anonymous list status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHealth(t *testing.T) {
	e := startServer(t, nil)
	resp, err := http.Get(e.url + "/health")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != `{"status":"ok"}` {
		t.Fatalf("health %d %s", resp.StatusCode, b)
	}
}

// Flysystem (Laravel) reads object ACLs to learn visibility on copy/move.
func TestObjectACL(t *testing.T) {
	e := startServer(t, func(c *config.Config) {
		c.Buckets["pub"] = config.BucketConfig{PublicRead: true}
		c.Buckets["priv"] = config.BucketConfig{}
	})
	for _, b := range []string{"pub", "priv"} {
		if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b), Key: aws.String("f"), Body: strings.NewReader("x")}); err != nil {
			t.Fatal(err)
		}
		acl, err := e.s3.GetObjectAcl(ctx, &s3.GetObjectAclInput{Bucket: aws.String(b), Key: aws.String("f")})
		if err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		public := false
		for _, g := range acl.Grants {
			if g.Grantee != nil && aws.ToString(g.Grantee.URI) == "http://acs.amazonaws.com/groups/global/AllUsers" && g.Permission == types.PermissionRead {
				public = true
			}
		}
		if public != (b == "pub") {
			t.Fatalf("bucket %s reported public=%v", b, public)
		}
		if _, err := e.s3.PutObjectAcl(ctx, &s3.PutObjectAclInput{Bucket: aws.String(b), Key: aws.String("f"), ACL: types.ObjectCannedACLPrivate}); err != nil {
			t.Fatalf("put acl: %v", err)
		}
	}
}

// A web page uploads with a presigned URL: preflight, then PUT with Origin.
func TestBrowserPresignedUploadCORS(t *testing.T) {
	e := startServer(t, func(c *config.Config) { c.CORSOrigins = []string{"https://app.example.com"} })
	if _, err := e.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("uploads")}); err != nil {
		t.Fatal(err)
	}
	u, _ := s3.NewPresignClient(e.s3).PresignPutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("uploads"), Key: aws.String("from-browser.txt")})
	pre, _ := http.NewRequest(http.MethodOptions, u.URL, nil)
	pre.Header.Set("Origin", "https://app.example.com")
	pre.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := http.DefaultClient.Do(pre)
	if err != nil || resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("preflight: %v %d", err, resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPut, u.URL, strings.NewReader("hello"))
	req.Header.Set("Origin", "https://app.example.com")
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") == "" || resp.Header.Get("ETag") == "" {
		t.Fatalf("browser PUT: %v %d %v", err, resp.StatusCode, resp.Header)
	}
	resp.Body.Close()
}

// Opening the bare address in a browser leads to the admin panel instead of
// an S3 AccessDenied XML; S3 clients are unaffected.
func TestBrowserVisitToRoot(t *testing.T) {
	e := startServer(t, nil)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", e.url+"/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := noFollow.Do(req)
	if err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/_admin/" {
		t.Fatalf("browser visit: %v %d %q", err, resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()
	// Anonymous non-browser request still gets the S3 error.
	resp, _ = http.Get(e.url + "/")
	if resp.StatusCode != 403 {
		t.Fatalf("anonymous S3 ListBuckets: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// SDK ListBuckets still works.
	if _, err := e.s3.ListBuckets(ctx, &s3.ListBucketsInput{}); err != nil {
		t.Fatal(err)
	}
	// Without the admin panel, a short explanation page is shown.
	e2 := startServer(t, func(c *config.Config) { c.Admin.Enabled = false })
	req, _ = http.NewRequest("GET", e2.url+"/", nil)
	req.Header.Set("Accept", "text/html")
	resp, _ = noFollow.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Tijori is running") {
		t.Fatalf("landing page: %d %s", resp.StatusCode, b)
	}
}

// A presigned upload URL must not be usable to copy another object: extra
// x-amz-* headers that were not signed are refused.
func TestPresignedURLCannotAddUnsignedHeaders(t *testing.T) {
	e := startServer(t, func(c *config.Config) { c.Buckets["avatars"] = config.BucketConfig{PublicRead: true} })
	if _, err := e.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("secret-docs")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("secret-docs"), Key: aws.String("payroll.csv"), Body: strings.NewReader("salaries")}); err != nil {
		t.Fatal(err)
	}
	u, _ := s3.NewPresignClient(e.s3).PresignPutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("avatars"), Key: aws.String("u1.jpg")})
	req, _ := http.NewRequest(http.MethodPut, u.URL, nil)
	req.Header.Set("X-Amz-Copy-Source", "/secret-docs/payroll.csv")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("unsigned copy-source accepted: %d", resp.StatusCode)
	}
	if r, _ := http.Get(e.url + "/avatars/u1.jpg"); r.StatusCode != 404 {
		t.Fatalf("object was created: %d", r.StatusCode)
	}
	// The same URL still works for its real purpose.
	req, _ = http.NewRequest(http.MethodPut, u.URL, strings.NewReader("avatar"))
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("normal presigned upload: %d", resp.StatusCode)
	}
}

// Unfinished multipart uploads count against the bucket quota.
func TestQuotaCountsUnfinishedUploads(t *testing.T) {
	e := startServer(t, func(c *config.Config) { c.Buckets["quota-b"] = config.BucketConfig{MaxSize: 1 << 20} })
	if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("quota-b"), Key: aws.String("base"), Body: bytes.NewReader(make([]byte, 900<<10))}); err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for i := 0; i < 5; i++ {
		cm, err := e.s3.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("quota-b"), Key: aws.String(fmt.Sprintf("u%d", i))})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.s3.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String("quota-b"), Key: aws.String(fmt.Sprintf("u%d", i)), UploadId: cm.UploadId,
			PartNumber: aws.Int32(1), Body: bytes.NewReader(make([]byte, 50<<10))})
		if err == nil {
			accepted++
		} else if errCode(err) != "QuotaExceeded" {
			t.Fatalf("unexpected error %v", err)
		}
	}
	// 900 KB used + 1 MB quota leaves room for two 50 KB parts, not five.
	if accepted != 2 {
		t.Fatalf("accepted %d parts beyond quota", accepted)
	}
}
