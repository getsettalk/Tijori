// Package s3err defines S3-style errors with their real S3 error codes so
// that SDKs recognise them.
package s3err

import "net/http"

// Error is an S3 API error.
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// WithMessage returns a copy of e with a different message.
func (e *Error) WithMessage(msg string) *Error {
	c := *e
	c.Message = msg
	return &c
}

func newErr(code, msg string, status int) *Error { return &Error{code, msg, status} }

var (
	AccessDenied                 = newErr("AccessDenied", "Access Denied.", http.StatusForbidden)
	SignatureDoesNotMatch        = newErr("SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided. Check your secret key and signing method.", http.StatusForbidden)
	InvalidAccessKeyID           = newErr("InvalidAccessKeyId", "The access key ID you provided does not exist in our records.", http.StatusForbidden)
	RequestTimeTooSkewed         = newErr("RequestTimeTooSkewed", "The difference between the request time and the server's time is too large (max 15 minutes). Check the clock on your computer or server.", http.StatusForbidden)
	ExpiredPresign               = newErr("AccessDenied", "Request has expired.", http.StatusForbidden)
	AuthorizationHeaderMalformed = newErr("AuthorizationHeaderMalformed", "The authorization header is malformed.", http.StatusBadRequest)
	AuthorizationQueryMalformed  = newErr("AuthorizationQueryParametersError", "Error parsing the X-Amz-Credential parameter.", http.StatusBadRequest)
	SignatureVersionNotSupported = newErr("InvalidRequest", "Only AWS Signature Version 4 is supported. Configure your client to use signature v4.", http.StatusBadRequest)
	ContentSHA256Mismatch        = newErr("XAmzContentSHA256Mismatch", "The provided 'x-amz-content-sha256' header does not match what was computed.", http.StatusBadRequest)
	MissingContentSHA256         = newErr("InvalidRequest", "Missing required header for this request: x-amz-content-sha256.", http.StatusBadRequest)
	InvalidChunk                 = newErr("IncompleteBody", "The aws-chunked request body is malformed.", http.StatusBadRequest)
	MissingContentLength         = newErr("MissingContentLength", "You must provide the Content-Length HTTP header.", http.StatusLengthRequired)
	NoSuchBucket                 = newErr("NoSuchBucket", "The specified bucket does not exist.", http.StatusNotFound)
	NoSuchKey                    = newErr("NoSuchKey", "The specified key does not exist.", http.StatusNotFound)
	NoSuchUpload                 = newErr("NoSuchUpload", "The specified multipart upload does not exist. The upload ID might be invalid, or the upload might have been aborted or completed.", http.StatusNotFound)
	BucketAlreadyOwnedByYou      = newErr("BucketAlreadyOwnedByYou", "Your previous request to create the named bucket succeeded and you already own it.", http.StatusConflict)
	BucketNotEmpty               = newErr("BucketNotEmpty", "The bucket you tried to delete is not empty.", http.StatusConflict)
	InvalidBucketName            = newErr("InvalidBucketName", "The specified bucket is not valid. Use 3-63 lowercase letters, digits, dots or hyphens.", http.StatusBadRequest)
	InvalidKey                   = newErr("InvalidArgument", "Object key is not allowed (it must not contain empty parts, '.', '..', backslashes or NUL).", http.StatusBadRequest)
	KeyTooLong                   = newErr("KeyTooLongError", "Your key is too long.", http.StatusBadRequest)
	PathConflict                 = newErr("InvalidArgument", "Object key conflicts with an existing object or folder of the same name (for example 'a' and 'a/b').", http.StatusConflict)
	InvalidArgument              = newErr("InvalidArgument", "Invalid argument.", http.StatusBadRequest)
	InvalidRequest               = newErr("InvalidRequest", "Invalid request.", http.StatusBadRequest)
	InvalidRange                 = newErr("InvalidRange", "The requested range is not satisfiable.", http.StatusRequestedRangeNotSatisfiable)
	BadDigest                    = newErr("BadDigest", "The Content-MD5 or checksum you specified did not match what we received.", http.StatusBadRequest)
	IncompleteBody               = newErr("IncompleteBody", "You did not provide the number of bytes specified by the Content-Length HTTP header.", http.StatusBadRequest)
	EntityTooLarge               = newErr("EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size.", http.StatusBadRequest)
	EntityTooSmall               = newErr("EntityTooSmall", "Your proposed upload is smaller than the minimum allowed object size. Each part must be at least 5 MB, except the last part.", http.StatusBadRequest)
	QuotaExceeded                = newErr("QuotaExceeded", "Storage quota exceeded.", http.StatusForbidden)
	InvalidPart                  = newErr("InvalidPart", "One or more of the specified parts could not be found. The part might not have been uploaded, or the specified ETag might not match the part's ETag.", http.StatusBadRequest)
	InvalidPartOrder             = newErr("InvalidPartOrder", "The list of parts was not in ascending order. Parts must be ordered by part number.", http.StatusBadRequest)
	MalformedXML                 = newErr("MalformedXML", "The XML you provided was not well-formed or did not validate against our published schema.", http.StatusBadRequest)
	MethodNotAllowed             = newErr("MethodNotAllowed", "The specified method is not allowed against this resource.", http.StatusMethodNotAllowed)
	NotImplemented               = newErr("NotImplemented", "A header or query you provided implies functionality that is not implemented.", http.StatusNotImplemented)
	PreconditionFailed           = newErr("PreconditionFailed", "At least one of the preconditions you specified did not hold.", http.StatusPreconditionFailed)
	InternalError                = newErr("InternalError", "We encountered an internal error. Please try again.", http.StatusInternalServerError)
	SlowDown                     = newErr("SlowDown", "Please reduce your request rate.", http.StatusServiceUnavailable)
)
