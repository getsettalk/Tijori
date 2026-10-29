package auth

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getsettalk/Tijori/internal/s3err"
	"github.com/getsettalk/Tijori/internal/state"
)

// Test vectors from the AWS S3 SigV4 documentation.
const (
	exampleAK = "AKIAIOSFODNN7EXAMPLE"
	exampleSK = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

type keyMap map[string]state.AccessKey

func (m keyMap) Key(id string) (state.AccessKey, bool) { k, ok := m[id]; return k, ok }

func testVerifier(now time.Time) *Verifier {
	return &Verifier{
		Keys: keyMap{exampleAK: {AccessKey: exampleAK, SecretKey: exampleSK, Buckets: []string{"*"}, Permission: state.PermReadWrite}},
		Now:  func() time.Time { return now },
	}
}

var vectorTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func TestHeaderAuthGetObjectVector(t *testing.T) {
	r := httptest.NewRequest("GET", "http://examplebucket.s3.amazonaws.com/test.txt", nil)
	r.Header.Set("Range", "bytes=0-9")
	r.Header.Set("X-Amz-Content-Sha256", emptySHA256)
	r.Header.Set("X-Amz-Date", "20130524T000000Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41")
	res, err := testVerifier(vectorTime).Verify(r)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Key == nil || res.Key.AccessKey != exampleAK {
		t.Fatalf("wrong key: %+v", res.Key)
	}

	// Same request, tampered header: must fail.
	r.Header.Set("Range", "bytes=0-10")
	if _, err := testVerifier(vectorTime).Verify(r); !errors.Is(err, s3err.SignatureDoesNotMatch) {
		t.Fatalf("expected SignatureDoesNotMatch, got %v", err)
	}
}

func TestPresignedVector(t *testing.T) {
	u := "http://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	r := httptest.NewRequest("GET", u, nil)
	if _, err := testVerifier(vectorTime.Add(time.Hour)).Verify(r); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := testVerifier(vectorTime.Add(25 * time.Hour)).Verify(r); !errors.Is(err, s3err.ExpiredPresign) {
		t.Fatalf("expected expiry error, got %v", err)
	}
}

func TestChunkedUploadVector(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 66560)
	var body bytes.Buffer
	body.WriteString("10000;chunk-signature=ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648\r\n")
	body.Write(data[:65536])
	body.WriteString("\r\n400;chunk-signature=0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497\r\n")
	body.Write(data[65536:])
	body.WriteString("\r\n0;chunk-signature=b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9\r\n\r\n")

	newReq := func(b []byte) *http.Request {
		r := httptest.NewRequest("PUT", "http://s3.amazonaws.com/examplebucket/chunkObject.txt", bytes.NewReader(b))
		r.Header.Set("X-Amz-Date", "20130524T000000Z")
		r.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")
		r.Header.Set("Content-Encoding", "aws-chunked")
		r.Header.Set("X-Amz-Decoded-Content-Length", "66560")
		r.Header.Set("Content-Length", "66824")
		r.Header.Set("X-Amz-Content-Sha256", streamingSigned)
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=content-encoding;content-length;host;x-amz-content-sha256;x-amz-date;x-amz-decoded-content-length;x-amz-storage-class,Signature=4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9")
		return r
	}
	res, err := testVerifier(vectorTime).Verify(newReq(body.Bytes()))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) || res.Size != 66560 {
		t.Fatalf("decoded %d bytes, size %d", len(got), res.Size)
	}

	// Corrupt one payload byte: the chunk signature must fail.
	bad := bytes.Clone(body.Bytes())
	bad[200] = 'b'
	res, err = testVerifier(vectorTime).Verify(newReq(bad))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := io.ReadAll(res.Body); !errors.Is(err, s3err.SignatureDoesNotMatch) {
		t.Fatalf("expected chunk signature failure, got %v", err)
	}
}

func TestPresignRoundTrip(t *testing.T) {
	now := time.Now()
	u, err := Presign(PresignOptions{Method: "PUT", URL: "http://localhost:9000/photos/a b+c.jpg", AccessKey: exampleAK, SecretKey: exampleSK, Expires: time.Minute, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PUT", u, strings.NewReader("hi"))
	if _, err := testVerifier(now).Verify(r); err != nil {
		t.Fatalf("verify own presigned URL: %v", err)
	}
	r = httptest.NewRequest("GET", u, nil) // method changed
	if _, err := testVerifier(now).Verify(r); !errors.Is(err, s3err.SignatureDoesNotMatch) {
		t.Fatalf("expected mismatch for different method, got %v", err)
	}
}

func TestClockSkew(t *testing.T) {
	r := httptest.NewRequest("GET", "http://examplebucket.s3.amazonaws.com/test.txt", nil)
	r.Header.Set("X-Amz-Date", "20130524T000000Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;x-amz-date,Signature=00")
	if _, err := testVerifier(vectorTime.Add(16 * time.Minute)).Verify(r); !errors.Is(err, s3err.RequestTimeTooSkewed) {
		t.Fatalf("expected RequestTimeTooSkewed, got %v", err)
	}
}

func TestPayloadHashMismatch(t *testing.T) {
	// A request whose declared SHA256 does not match the body fails on read.
	h := &hashReader{r: io.NopCloser(strings.NewReader("hello")), h: sha256.New(), want: emptySHA256}
	if _, err := io.ReadAll(h); !errors.Is(err, s3err.ContentSHA256Mismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

func TestNewKeyFormat(t *testing.T) {
	k, err := NewKey("x", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.AccessKey, "tj_") || len(k.AccessKey) != 19 || len(k.SecretKey) != 40 {
		t.Fatalf("bad key format %q / %d", k.AccessKey, len(k.SecretKey))
	}
	if _, err := NewKey("x", nil, "admin"); err == nil {
		t.Fatal("expected error for bad permission")
	}
}
