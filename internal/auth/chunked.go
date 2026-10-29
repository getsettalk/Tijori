package auth

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"hash/crc32"
	"io"
	"strconv"
	"strings"

	"github.com/getsettalk/Tijori/internal/s3err"
)

type chunkConfig struct {
	signed     bool
	trailer    bool
	signingKey []byte
	seed       string // signature of the request headers
	amzDate    string
	scope      string
	trailerHdr string // value of x-amz-trailer, e.g. "x-amz-checksum-crc32"
}

// chunkedReader decodes an aws-chunked body, verifying each chunk signature
// (for signed streaming) and the trailing checksum when present. Data is
// passed through before a chunk's signature is checked, so callers must not
// commit anything until they have read to io.EOF without error.
type chunkedReader struct {
	cfg       chunkConfig
	src       io.ReadCloser
	r         *bufio.Reader
	prevSig   string
	remaining int64
	chunkSig  string
	chunkHash hash.Hash
	checksum  hash.Hash
	checkName string
	err       error
}

const maxChunkLine = 4096

func newChunkedReader(body io.ReadCloser, cfg chunkConfig) *chunkedReader {
	c := &chunkedReader{cfg: cfg, src: body, r: bufio.NewReaderSize(body, 64<<10), prevSig: cfg.seed, chunkHash: sha256.New()}
	c.checkName = strings.TrimPrefix(cfg.trailerHdr, "x-amz-checksum-")
	switch c.checkName {
	case "crc32":
		c.checksum = crc32.NewIEEE()
	case "crc32c":
		c.checksum = crc32.New(crc32.MakeTable(crc32.Castagnoli))
	case "sha1":
		c.checksum = sha1.New()
	case "sha256":
		c.checksum = sha256.New()
	}
	return c
}

func (c *chunkedReader) Close() error { return c.src.Close() }

func (c *chunkedReader) readLine() (string, error) {
	line, err := c.r.ReadSlice('\n')
	if err == bufio.ErrBufferFull || len(line) > maxChunkLine {
		return "", s3err.InvalidChunk
	}
	if err != nil {
		if err == io.EOF && len(line) == 0 {
			return "", io.ErrUnexpectedEOF
		}
		if err != io.EOF {
			return "", err
		}
	}
	return strings.TrimRight(string(line), "\r\n"), nil
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.remaining == 0 {
		if err := c.nextChunk(); err != nil {
			c.err = err
			return 0, err
		}
	}
	if len(p) == 0 {
		return 0, nil
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	if c.cfg.signed {
		c.chunkHash.Write(p[:n])
	}
	if c.checksum != nil {
		c.checksum.Write(p[:n])
	}
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		c.err = err
		return n, err
	}
	if c.remaining == 0 {
		if err := c.endChunk(); err != nil {
			c.err = err
			return n, err
		}
	}
	return n, nil
}

// nextChunk reads a chunk header. At the final zero-length chunk it reads
// trailers and returns io.EOF.
func (c *chunkedReader) nextChunk() error {
	line, err := c.readLine()
	if err != nil {
		return s3err.IncompleteBody
	}
	sizeStr, ext, _ := strings.Cut(line, ";")
	size, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 16, 64)
	if err != nil || size < 0 {
		return s3err.InvalidChunk
	}
	if c.cfg.signed {
		sig, ok := strings.CutPrefix(strings.TrimSpace(ext), "chunk-signature=")
		if !ok || sig == "" {
			return s3err.InvalidChunk
		}
		c.chunkSig = sig
	}
	if size == 0 {
		if err := c.verifyChunk(); err != nil {
			return err
		}
		if c.cfg.trailer {
			if err := c.readTrailer(); err != nil {
				return err
			}
		} else {
			// Final CRLF after the empty chunk; tolerate its absence.
			_, _ = c.readLine()
		}
		return io.EOF
	}
	c.remaining = size
	return nil
}

func (c *chunkedReader) endChunk() error {
	var crlf [2]byte
	if _, err := io.ReadFull(c.r, crlf[:]); err != nil || crlf != [2]byte{'\r', '\n'} {
		return s3err.InvalidChunk
	}
	return c.verifyChunk()
}

func (c *chunkedReader) verifyChunk() error {
	if !c.cfg.signed {
		return nil
	}
	sts := "AWS4-HMAC-SHA256-PAYLOAD\n" + c.cfg.amzDate + "\n" + c.cfg.scope + "\n" + c.prevSig + "\n" +
		emptySHA256 + "\n" + hex.EncodeToString(c.chunkHash.Sum(nil))
	want := signature(c.cfg.signingKey, sts)
	if !hmac.Equal([]byte(want), []byte(c.chunkSig)) {
		return s3err.SignatureDoesNotMatch
	}
	c.prevSig = c.chunkSig
	c.chunkHash.Reset()
	return nil
}

func (c *chunkedReader) readTrailer() error {
	var canonical bytes.Buffer
	var trailerSig string
	headers := map[string]string{}
	for {
		line, err := c.readLine()
		if err != nil || line == "" {
			break
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			return s3err.InvalidChunk
		}
		name = strings.ToLower(strings.TrimSpace(name))
		val = strings.TrimSpace(val)
		if name == "x-amz-trailer-signature" {
			trailerSig = val
			continue
		}
		headers[name] = val
		canonical.WriteString(name + ":" + val + "\n")
	}
	if c.cfg.signed {
		h := sha256.Sum256(canonical.Bytes())
		sts := "AWS4-HMAC-SHA256-TRAILER\n" + c.cfg.amzDate + "\n" + c.cfg.scope + "\n" + c.prevSig + "\n" + hex.EncodeToString(h[:])
		if !hmac.Equal([]byte(signature(c.cfg.signingKey, sts)), []byte(trailerSig)) {
			return s3err.SignatureDoesNotMatch
		}
	}
	if c.checksum != nil {
		if got, ok := headers["x-amz-checksum-"+c.checkName]; ok {
			if base64.StdEncoding.EncodeToString(c.checksum.Sum(nil)) != got {
				return s3err.BadDigest.WithMessage("The " + strings.ToUpper(c.checkName) + " checksum you specified did not match the calculated checksum.")
			}
		}
	}
	return nil
}
