package objectstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 for the S3 service, implemented directly so the
// module stays free of cloud SDK dependency trees. Bodies are signed as
// UNSIGNED-PAYLOAD (transport integrity comes from TLS; object integrity from
// the caller's content digests), which keeps request bodies streamable.

const (
	sigAlgorithm    = "AWS4-HMAC-SHA256"
	sigService      = "s3"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	emptyPayloadSHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type signer struct {
	keyID  string
	secret string
	region string
}

// signHeader adds X-Amz-Date, X-Amz-Content-Sha256, and Authorization headers
// to req. payloadSHA should be unsignedPayload for streamed bodies or the hex
// SHA-256 of the exact body otherwise.
func (s signer) signHeader(req *http.Request, now time.Time, payloadSHA string) {
	amzDate := now.UTC().Format("20060102T150405Z")
	shortDate := now.UTC().Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadSHA)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{
		"host":                 host,
		"x-amz-date":           amzDate,
		"x-amz-content-sha256": payloadSHA,
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headers["content-type"] = ct
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	signedHeaders := strings.Join(names, ";")

	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(headers[name]))
		canonicalHeaders.WriteString("\n")
	}

	canonical := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL.Query()),
		canonicalHeaders.String(),
		signedHeaders,
		payloadSHA,
	}, "\n")

	scope := shortDate + "/" + s.region + "/" + sigService + "/aws4_request"
	stringToSign := strings.Join([]string{
		sigAlgorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonical)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(s.signingKey(shortDate), []byte(stringToSign)))

	req.Header.Set("Authorization", sigAlgorithm+
		" Credential="+s.keyID+"/"+scope+
		", SignedHeaders="+signedHeaders+
		", Signature="+signature)
}

// presignURL returns a query-authenticated URL for method on rawURL.
func (s signer) presignURL(method string, u *url.URL, host string, now time.Time, expiry time.Duration) string {
	amzDate := now.UTC().Format("20060102T150405Z")
	shortDate := now.UTC().Format("20060102")
	scope := shortDate + "/" + s.region + "/" + sigService + "/aws4_request"

	q := u.Query()
	q.Set("X-Amz-Algorithm", sigAlgorithm)
	q.Set("X-Amz-Credential", s.keyID+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", itoa(int64(expiry.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")

	canonical := strings.Join([]string{
		method,
		canonicalURI(u),
		canonicalQuery(q),
		"host:" + host + "\n",
		"host",
		unsignedPayload,
	}, "\n")
	stringToSign := strings.Join([]string{
		sigAlgorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonical)),
	}, "\n")
	q.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA256(s.signingKey(shortDate), []byte(stringToSign))))

	signed := *u
	signed.RawQuery = encodeQuery(q)
	return signed.String()
}

func (s signer) signingKey(shortDate string) []byte {
	k := hmacSHA256([]byte("AWS4"+s.secret), []byte(shortDate))
	k = hmacSHA256(k, []byte(s.region))
	k = hmacSHA256(k, []byte(sigService))
	return hmacSHA256(k, []byte("aws4_request"))
}

// canonicalURI percent-encodes each path segment per SigV4 rules (S3 style:
// the path is encoded once, slashes preserved).
func canonicalURI(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			decoded = segment
		}
		segments[i] = uriEncode(decoded, false)
	}
	return strings.Join(segments, "/")
}

func canonicalQuery(q url.Values) string {
	return encodeQuery(q)
}

// encodeQuery renders values sorted by key then value, SigV4-encoded.
func encodeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		values := append([]string(nil), q[k]...)
		sort.Strings(values)
		for _, v := range values {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode implements the SigV4 variant of RFC 3986 percent-encoding:
// unreserved characters pass through, everything else (including '/' when
// encodeSlash) becomes %XX with uppercase hex.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			const hexDigits = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		}
	}
	return b.String()
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func itoa(v int64) string {
	if v <= 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
