package s3api

import (
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/getsettalk/Tijori/internal/s3err"
)

// jsonError is the JSON form of an S3 error, for clients that ask for it.
type jsonError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Status    int    `json:"status"`
		Bucket    string `json:"bucket,omitempty"`
		Key       string `json:"key,omitempty"`
		Resource  string `json:"resource"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func writeJSONError(w http.ResponseWriter, e *s3err.Error, req *request) {
	var out jsonError
	out.Error.Code, out.Error.Message, out.Error.Status = e.Code, e.Message, e.Status
	out.Error.Bucket, out.Error.Key = req.bucket, req.key
	out.Error.Resource, out.Error.RequestID = req.r.URL.Path, req.id
	b, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(e.Status)
	_, _ = w.Write(b)
}

// WantsJSON reports whether an Accept header prefers JSON over XML. A bare
// "*/*" (what most tools and S3 SDKs send) keeps XML.
func WantsJSON(accept string) bool {
	if accept == "" {
		return false
	}
	jsonQ, xmlQ := -1.0, -1.0
	for _, part := range strings.Split(accept, ",") {
		mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		switch {
		case mt == "application/json" || strings.HasSuffix(mt, "+json"):
			jsonQ = max(jsonQ, q)
		case mt == "application/xml" || mt == "text/xml":
			xmlQ = max(xmlQ, q)
		}
	}
	return jsonQ > 0 && jsonQ >= xmlQ
}
