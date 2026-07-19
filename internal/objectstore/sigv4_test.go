package objectstore

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestPresignAWSVector reproduces the worked example from the AWS SigV4
// documentation ("Authenticating Requests: Using Query Parameters"), which
// pins the canonical-request construction, signing-key derivation, and query
// encoding against a known-good signature.
func TestPresignAWSVector(t *testing.T) {
	s := signer{
		keyID:  "AKIAIOSFODNN7EXAMPLE",
		secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		region: "us-east-1",
	}
	u, err := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	signed := s.presignURL("GET", u, "examplebucket.s3.amazonaws.com", at, 86400*time.Second)

	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("presigned URL does not parse: %v", err)
	}
	got := parsed.Query().Get("X-Amz-Signature")
	const want = "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got != want {
		t.Fatalf("signature mismatch\n got %s\nwant %s\nurl %s", got, want, signed)
	}
	if !strings.Contains(signed, "X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request") {
		t.Fatalf("credential scope not canonically encoded: %s", signed)
	}
}

func TestURIEncode(t *testing.T) {
	cases := []struct {
		in          string
		encodeSlash bool
		want        string
	}{
		{"simple-key_1.txt~", true, "simple-key_1.txt~"},
		{"a b+c", true, "a%20b%2Bc"},
		{"sources/abc/bundle.git", false, "sources/abc/bundle.git"},
		{"sources/abc/bundle.git", true, "sources%2Fabc%2Fbundle.git"},
		{"emoji=é", true, "emoji%3D%C3%A9"},
	}
	for _, tc := range cases {
		if got := uriEncode(tc.in, tc.encodeSlash); got != tc.want {
			t.Errorf("uriEncode(%q, %v) = %q, want %q", tc.in, tc.encodeSlash, got, tc.want)
		}
	}
}

func TestCanonicalQuerySortsKeysAndValues(t *testing.T) {
	q := url.Values{}
	q.Add("b", "2")
	q.Add("a", "z")
	q.Add("a", "a")
	if got, want := canonicalQuery(q), "a=a&a=z&b=2"; got != want {
		t.Fatalf("canonicalQuery = %q, want %q", got, want)
	}
}
