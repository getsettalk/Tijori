// Package backup copies Tijori's data to a local folder, an SSH server
// (sftp://) or any S3-compatible service (s3://), restores it, and packs
// everything into a single .tar.gz for moving servers.
package backup

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/getsettalk/Tijori/internal/auth"
)

// ErrNotFound is returned by Target.Get for missing files.
var ErrNotFound = errors.New("not found")

// Target is a backup destination. Paths are slash-separated and relative.
type Target interface {
	Put(p string, r io.Reader, size int64) error
	Get(p string) (io.ReadCloser, error)
	Delete(p string) error
	// ListDirs returns the names of sub-directories of dir.
	ListDirs(dir string) ([]string, error)
	String() string
}

// Open parses a destination string.
func Open(dest string) (Target, error) {
	switch {
	case strings.HasPrefix(dest, "s3://"):
		return openS3(dest)
	case strings.HasPrefix(dest, "sftp://"), strings.HasPrefix(dest, "ssh://"):
		return openSSH(dest)
	case strings.HasPrefix(dest, "file://"):
		return &localTarget{root: strings.TrimPrefix(dest, "file://")}, nil
	case strings.Contains(dest, "://"):
		return nil, fmt.Errorf("unknown backup destination %q (use a folder path, sftp://user@host/path or s3://bucket?endpoint=...)", dest)
	case dest == "":
		return nil, errors.New("no backup destination given")
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return nil, err
	}
	return &localTarget{root: abs}, nil
}

func cleanRel(p string) (string, error) {
	c := path.Clean("/" + p)[1:]
	if c == "" || c == "." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("invalid backup path %q", p)
	}
	return c, nil
}

// ---- local folder -----------------------------------------------------------

type localTarget struct{ root string }

func (t *localTarget) String() string { return t.root }

func (t *localTarget) path(p string) (string, error) {
	c, err := cleanRel(p)
	if err != nil {
		return "", err
	}
	return filepath.Join(t.root, filepath.FromSlash(c)), nil
}

func (t *localTarget) Put(p string, r io.Reader, size int64) error {
	dst, err := t.path(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".tijori-backup-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), dst)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func (t *localTarget) Get(p string) (io.ReadCloser, error) {
	src, err := t.path(p)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

func (t *localTarget) Delete(p string) error {
	dst, err := t.path(p)
	if err != nil {
		return err
	}
	err = os.Remove(dst)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		// Remove empty parents up to the root.
		for d := filepath.Dir(dst); d != t.root && strings.HasPrefix(d, t.root); d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
	return err
}

func (t *localTarget) ListDirs(dir string) ([]string, error) {
	d, err := t.path(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(d)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, err
}

// ---- SSH (sftp://user@host:port/path) ---------------------------------------

// sshTarget runs commands over the system ssh client, so the user's normal
// SSH keys and ~/.ssh/config apply. One multiplexed connection is reused.
type sshTarget struct {
	host, port, root string
	control          string
}

func openSSH(dest string) (*sshTarget, error) {
	u, err := url.Parse(dest)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid sftp destination %q (example: sftp://backup@server.example.com/srv/tijori)", dest)
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, errors.New("sftp:// backups need the ssh command; install OpenSSH client")
	}
	host := u.Hostname()
	if u.User != nil {
		host = u.User.Username() + "@" + host
	}
	root := strings.TrimSuffix(u.Path, "/")
	if root == "" {
		root = "tijori-backup"
	} else if strings.HasPrefix(root, "/~/") {
		root = root[3:]
	}
	ctl := filepath.Join(os.TempDir(), fmt.Sprintf("tijori-ssh-%d", os.Getpid()))
	return &sshTarget{host: host, port: u.Port(), root: root, control: ctl}, nil
}

func (t *sshTarget) String() string {
	return "sftp://" + t.host + "/" + strings.TrimPrefix(t.root, "/")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (t *sshTarget) run(cmd string, stdin io.Reader, stdout io.Writer) error {
	args := []string{"-o", "BatchMode=yes", "-o", "ControlMaster=auto", "-o", "ControlPath=" + t.control, "-o", "ControlPersist=60"}
	if t.port != "" {
		args = append(args, "-p", t.port)
	}
	args = append(args, t.host, cmd)
	c := exec.Command("ssh", args...)
	c.Stdin = stdin
	c.Stdout = stdout
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ssh %s: %s", t.host, msg)
	}
	return nil
}

func (t *sshTarget) full(p string) (string, error) {
	c, err := cleanRel(p)
	if err != nil {
		return "", err
	}
	return t.root + "/" + c, nil
}

func (t *sshTarget) Put(p string, r io.Reader, size int64) error {
	dst, err := t.full(p)
	if err != nil {
		return err
	}
	tmp := dst + ".tijori-tmp"
	return t.run(fmt.Sprintf("mkdir -p %s && cat > %s && mv -f %s %s", shellQuote(path.Dir(dst)), shellQuote(tmp), shellQuote(tmp), shellQuote(dst)), r, nil)
}

func (t *sshTarget) Get(p string) (io.ReadCloser, error) {
	src, err := t.full(p)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.run(fmt.Sprintf("test -f %s || exit 3; cat %s", shellQuote(src), shellQuote(src)), nil, &buf); err != nil {
		var ee *exec.ExitError
		if errors.As(errors.Unwrap(err), &ee) || strings.Contains(err.Error(), "exit status 3") {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return io.NopCloser(&buf), nil
}

func (t *sshTarget) Delete(p string) error {
	dst, err := t.full(p)
	if err != nil {
		return err
	}
	return t.run("rm -f "+shellQuote(dst), nil, nil)
}

func (t *sshTarget) ListDirs(dir string) ([]string, error) {
	d, err := t.full(dir)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.run(fmt.Sprintf("cd %s 2>/dev/null && for f in *; do [ -d \"$f\" ] && echo \"$f\"; done; true", shellQuote(d)), nil, &buf); err != nil {
		return nil, err
	}
	return strings.Fields(buf.String()), nil
}

// ---- S3 (s3://bucket/prefix?endpoint=https://...) ---------------------------

type s3Target struct {
	endpoint, bucket, prefix, region string
	ak, sk                           string
	client                           *http.Client
}

func openS3(dest string) (*s3Target, error) {
	u, err := url.Parse(dest)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid s3 destination %q (example: s3://my-backups?endpoint=https://s3.eu-central-003.backblazeb2.com)", dest)
	}
	q := u.Query()
	t := &s3Target{
		bucket: u.Host, prefix: strings.Trim(u.Path, "/"), endpoint: strings.TrimSuffix(q.Get("endpoint"), "/"),
		region: q.Get("region"), ak: q.Get("access_key"), sk: q.Get("secret_key"),
		client: &http.Client{Timeout: 30 * time.Minute},
	}
	if t.endpoint == "" {
		return nil, errors.New("s3 destination needs ?endpoint=https://... (for AWS use https://s3.<region>.amazonaws.com)")
	}
	for _, pair := range [][2]string{{"TIJORI_BACKUP_ACCESS_KEY", "TIJORI_BACKUP_SECRET_KEY"}, {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}} {
		if t.ak == "" {
			t.ak, t.sk = os.Getenv(pair[0]), os.Getenv(pair[1])
		}
	}
	if t.ak == "" || t.sk == "" {
		return nil, errors.New("s3 destination needs credentials: set TIJORI_BACKUP_ACCESS_KEY and TIJORI_BACKUP_SECRET_KEY")
	}
	if t.region == "" {
		t.region = "auto"
	}
	return t, nil
}

func (t *s3Target) String() string {
	s := "s3://" + t.bucket
	if t.prefix != "" {
		s += "/" + t.prefix
	}
	return s + "?endpoint=" + t.endpoint
}

func (t *s3Target) key(p string) (string, error) {
	c, err := cleanRel(p)
	if err != nil {
		return "", err
	}
	if t.prefix != "" {
		c = t.prefix + "/" + c
	}
	return c, nil
}

func (t *s3Target) do(method, key string, query url.Values, body io.Reader, size int64) (*http.Response, error) {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := t.endpoint + "/" + t.bucket
	if key != "" {
		u += "/" + strings.Join(segs, "/")
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	auth.SignRequest(req, t.ak, t.sk, t.region, time.Now())
	return t.client.Do(req)
}

func s3Fail(resp *http.Response, what string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(b, &e) == nil && e.Code != "" {
		return fmt.Errorf("%s: %s: %s", what, e.Code, e.Message)
	}
	return fmt.Errorf("%s: HTTP %d", what, resp.StatusCode)
}

func (t *s3Target) Put(p string, r io.Reader, size int64) error {
	k, err := t.key(p)
	if err != nil {
		return err
	}
	if size > 5<<30 {
		return fmt.Errorf("%s is larger than 5 GB, the single-upload limit for s3:// backups; use a folder or sftp:// destination", p)
	}
	resp, err := t.do(http.MethodPut, k, nil, r, size)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return s3Fail(resp, "upload "+p)
	}
	return nil
}

func (t *s3Target) Get(p string) (io.ReadCloser, error) {
	k, err := t.key(p)
	if err != nil {
		return nil, err
	}
	resp, err := t.do(http.MethodGet, k, nil, nil, 0)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, s3Fail(resp, "download "+p)
	}
	return resp.Body, nil
}

func (t *s3Target) Delete(p string) error {
	k, err := t.key(p)
	if err != nil {
		return err
	}
	resp, err := t.do(http.MethodDelete, k, nil, nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return s3Fail(resp, "delete "+p)
	}
	return nil
}

func (t *s3Target) ListDirs(dir string) ([]string, error) {
	k, err := t.key(dir)
	if err != nil {
		return nil, err
	}
	prefix := k + "/"
	var out []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "delimiter": {"/"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := t.do(http.MethodGet, "", q, nil, 0)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 {
			err := s3Fail(resp, "list "+dir)
			resp.Body.Close()
			return nil, err
		}
		var res struct {
			CommonPrefixes []struct{ Prefix string } `xml:"CommonPrefixes"`
			IsTruncated    bool                      `xml:"IsTruncated"`
			Next           string                    `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, cp := range res.CommonPrefixes {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(cp.Prefix, prefix), "/"))
		}
		if !res.IsTruncated || res.Next == "" {
			return out, nil
		}
		token = res.Next
	}
}
