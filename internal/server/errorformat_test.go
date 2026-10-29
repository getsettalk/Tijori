package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/getsettalk/Tijori/internal/config"
)

func getWithAccept(t *testing.T, url, accept string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestErrorFormat(t *testing.T) {
	for _, mode := range []string{"auto", "xml", "json"} {
		t.Run(mode, func(t *testing.T) {
			e := startServer(t, func(c *config.Config) {
				c.ErrorFormat = mode
				c.Buckets["pics"] = config.BucketConfig{PublicRead: true}
			})
			url := e.url + "/pics/missing.jpg"
			for _, accept := range []string{"", "application/json"} {
				resp, body := getWithAccept(t, url, accept)
				wantJSON := mode == "json" || (mode == "auto" && accept != "")
				if resp.StatusCode != 404 {
					t.Fatalf("status %d", resp.StatusCode)
				}
				if wantJSON {
					var out struct {
						Error struct {
							Code      string `json:"code"`
							Bucket    string `json:"bucket"`
							RequestID string `json:"request_id"`
						} `json:"error"`
					}
					if err := json.Unmarshal([]byte(body), &out); err != nil || out.Error.Code != "NoSuchKey" || out.Error.Bucket != "pics" || out.Error.RequestID == "" {
						t.Fatalf("accept %q: expected JSON NoSuchKey, got %s", accept, body)
					}
					if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
						t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
					}
				} else if !strings.Contains(body, "<Code>NoSuchKey</Code>") {
					t.Fatalf("accept %q: expected XML, got %s", accept, body)
				}
			}
			// HEAD errors never have a body, in any mode.
			req, _ := http.NewRequest(http.MethodHead, url, nil)
			req.Header.Set("Accept", "application/json")
			resp, _ := http.DefaultClient.Do(req)
			if resp.StatusCode != 404 || resp.ContentLength > 0 {
				t.Fatalf("HEAD: %d len %d", resp.StatusCode, resp.ContentLength)
			}
			// In auto mode the Go SDK still gets XML and recognises the code.
			if mode == "auto" {
				_, err := e.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("pics"), Key: aws.String("missing.jpg")})
				if errCode(err) != "NoSuchKey" {
					t.Fatalf("SDK error code: %v", err)
				}
			}
		})
	}
}
