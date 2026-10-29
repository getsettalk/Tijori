package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/video"
)

func TestVideoProcessing(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	if out, err := exec.Command(ff, "-y", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=1280x960:rate=15",
		"-f", "lavfi", "-i", "sine=duration=2", "-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", clip).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot make a test clip: %s", out)
	}

	hooks := make(chan *http.Request, 1)
	bodies := make(chan []byte, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hooks <- r
		bodies <- b
	}))
	defer hook.Close()

	e := startServer(t, func(c *config.Config) {
		c.Buckets["videos"] = config.BucketConfig{VideoProcess: true}
		c.Video.Webhook = hook.URL
		c.Video.WebhookSecret = "s3cret"
	})
	data, _ := os.ReadFile(clip)
	if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("videos"), Key: aws.String("trips/goa.mp4"),
		Body: bytes.NewReader(data), ContentType: aws.String("video/mp4")}); err != nil {
		t.Fatal(err)
	}

	var req *http.Request
	var body []byte
	select {
	case req = <-hooks:
		body = <-bodies
	case <-time.After(60 * time.Second):
		t.Fatal("no webhook within 60s")
	}
	if got, want := req.Header.Get("X-Tijori-Signature"), video.Sign([]byte("s3cret"), body); got != want {
		t.Fatalf("bad signature %q want %q", got, want)
	}
	var ev struct {
		Event   string         `json:"event"`
		Error   string         `json:"error"`
		Outputs []video.Output `json:"outputs"`
	}
	_ = json.Unmarshal(body, &ev)
	if ev.Event != "video.processed" || len(ev.Outputs) != 2 {
		t.Fatalf("webhook payload: %s", body)
	}
	for _, k := range []string{"trips/goa.jpg", "trips/goa_720p.mp4"} {
		head, err := e.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("videos"), Key: aws.String(k)})
		if err != nil || aws.ToInt64(head.ContentLength) == 0 {
			t.Fatalf("output %s missing: %v", k, err)
		}
	}
	// The 720p output must not trigger another job.
	time.Sleep(300 * time.Millisecond)
	if jobs, _ := e.srv.Video.Jobs(); len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
}
