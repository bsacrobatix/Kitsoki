package objectstore

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// SpacesConfig describes an S3-compatible bucket endpoint. Endpoint is the
// regional endpoint (e.g. https://sgp1.digitaloceanspaces.com); requests use
// virtual-hosted style (bucket.region.host). Credentials are resolved from
// the named environment variables so secrets stay out of config files.
type SpacesConfig struct {
	Endpoint  string `yaml:"endpoint" json:"endpoint"`
	Bucket    string `yaml:"bucket" json:"bucket"`
	Region    string `yaml:"region" json:"region"`
	KeyEnv    string `yaml:"key_env" json:"key_env"`
	SecretEnv string `yaml:"secret_env" json:"secret_env"`
}

// ParseBucketURL accepts either a regional endpoint plus explicit bucket, or a
// virtual-hosted bucket URL like https://kitsoki-test.sgp1.digitaloceanspaces.com
// and splits it into bucket, region, and regional endpoint.
func ParseBucketURL(raw string) (SpacesConfig, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return SpacesConfig{}, fmt.Errorf("objectstore: invalid bucket URL %q", raw)
	}
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) < 4 {
		return SpacesConfig{}, fmt.Errorf("objectstore: bucket URL %q is not virtual-hosted (want bucket.region.host)", raw)
	}
	return SpacesConfig{
		Endpoint: u.Scheme + "://" + strings.Join(labels[1:], "."),
		Bucket:   labels[0],
		Region:   labels[1],
	}, nil
}

// Spaces is a Store backed by an S3-compatible service.
type Spaces struct {
	cfg    SpacesConfig
	sign   signer
	client *http.Client
	base   *url.URL
	now    func() time.Time
}

// NewSpaces builds a client from cfg, reading credentials from cfg.KeyEnv /
// cfg.SecretEnv. httpClient may be nil for http.DefaultClient.
func NewSpaces(cfg SpacesConfig, httpClient *http.Client) (*Spaces, error) {
	if cfg.Bucket == "" || cfg.Endpoint == "" {
		return nil, fmt.Errorf("objectstore: spaces config requires endpoint and bucket")
	}
	if cfg.Region == "" {
		if parsed, err := ParseBucketURL(cfg.Endpoint); err == nil {
			cfg.Region = parsed.Region
		} else if labels := strings.Split(hostOf(cfg.Endpoint), "."); len(labels) >= 3 {
			cfg.Region = labels[0]
		}
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("objectstore: spaces config requires region")
	}
	if cfg.KeyEnv == "" || cfg.SecretEnv == "" {
		return nil, fmt.Errorf("objectstore: spaces config requires key_env and secret_env")
	}
	keyID, secret := os.Getenv(cfg.KeyEnv), os.Getenv(cfg.SecretEnv)
	if keyID == "" || secret == "" {
		return nil, fmt.Errorf("objectstore: credentials missing: set %s and %s", cfg.KeyEnv, cfg.SecretEnv)
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("objectstore: invalid endpoint %q", cfg.Endpoint)
	}
	base := *endpoint
	base.Host = cfg.Bucket + "." + endpoint.Host
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Spaces{
		cfg:    cfg,
		sign:   signer{keyID: keyID, secret: secret, region: cfg.Region},
		client: httpClient,
		base:   &base,
		now:    time.Now,
	}, nil
}

func (s *Spaces) objectURL(key string) *url.URL {
	u := *s.base
	u.Path = "/" + key
	return &u
}

func (s *Spaces) do(ctx context.Context, method, key string, query url.Values, body io.Reader, size int64, contentType string) (*http.Response, error) {
	u := s.objectURL(key)
	if query != nil {
		// The wire encoding must byte-match the SigV4 canonical form, so the
		// signature the server recomputes agrees with ours.
		u.RawQuery = encodeQuery(query)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	payloadSHA := emptyPayloadSHA
	if body != nil {
		payloadSHA = unsignedPayload
	}
	s.sign.signHeader(req, s.now(), payloadSHA)
	return s.client.Do(req)
}

func (s *Spaces) Put(ctx context.Context, key string, body io.Reader, size int64, opts PutOptions) (Meta, error) {
	if err := validateKey(key); err != nil {
		return Meta{}, err
	}
	if size < 0 {
		return Meta{}, fmt.Errorf("objectstore: negative size for %q", key)
	}
	resp, err := s.do(ctx, http.MethodPut, key, nil, body, size, opts.ContentType)
	if err != nil {
		return Meta{}, fmt.Errorf("objectstore: put %s: %w", key, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Meta{}, httpError("put", key, resp)
	}
	return Meta{
		Key:          key,
		Size:         size,
		ETag:         strings.Trim(resp.Header.Get("ETag"), `"`),
		ContentType:  opts.ContentType,
		LastModified: s.now().UTC(),
	}, nil
}

func (s *Spaces) Get(ctx context.Context, key string) (io.ReadCloser, Meta, error) {
	if err := validateKey(key); err != nil {
		return nil, Meta{}, err
	}
	resp, err := s.do(ctx, http.MethodGet, key, nil, nil, 0, "")
	if err != nil {
		return nil, Meta{}, fmt.Errorf("objectstore: get %s: %w", key, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		drainClose(resp.Body)
		return nil, Meta{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if resp.StatusCode != http.StatusOK {
		defer drainClose(resp.Body)
		return nil, Meta{}, httpError("get", key, resp)
	}
	return resp.Body, metaFromResponse(key, resp), nil
}

func (s *Spaces) Head(ctx context.Context, key string) (Meta, error) {
	if err := validateKey(key); err != nil {
		return Meta{}, err
	}
	resp, err := s.do(ctx, http.MethodHead, key, nil, nil, 0, "")
	if err != nil {
		return Meta{}, fmt.Errorf("objectstore: head %s: %w", key, err)
	}
	defer drainClose(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return metaFromResponse(key, resp), nil
	case http.StatusNotFound, http.StatusForbidden:
		// Spaces answers 403 for HEAD on missing keys when the credential
		// lacks list permission, matching S3 semantics.
		return Meta{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	default:
		return Meta{}, httpError("head", key, resp)
	}
}

type listBucketResult struct {
	Contents []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		ETag         string `xml:"ETag"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

func (s *Spaces) List(ctx context.Context, prefix string) ([]Meta, error) {
	var out []Meta
	continuation := ""
	for {
		query := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if continuation != "" {
			query.Set("continuation-token", continuation)
		}
		resp, err := s.do(ctx, http.MethodGet, "", query, nil, 0, "")
		if err != nil {
			return nil, fmt.Errorf("objectstore: list %s: %w", prefix, err)
		}
		if resp.StatusCode != http.StatusOK {
			defer drainClose(resp.Body)
			return nil, httpError("list", prefix, resp)
		}
		var page listBucketResult
		err = xml.NewDecoder(resp.Body).Decode(&page)
		drainClose(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("objectstore: list %s: decode: %w", prefix, err)
		}
		for _, item := range page.Contents {
			meta := Meta{Key: item.Key, Size: item.Size, ETag: strings.Trim(item.ETag, `"`)}
			if ts, parseErr := time.Parse(time.RFC3339, item.LastModified); parseErr == nil {
				meta.LastModified = ts
			}
			out = append(out, meta)
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		continuation = page.NextContinuationToken
	}
}

func (s *Spaces) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodDelete, key, nil, nil, 0, "")
	if err != nil {
		return fmt.Errorf("objectstore: delete %s: %w", key, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return httpError("delete", key, resp)
	}
	return nil
}

func (s *Spaces) Presign(ctx context.Context, method, key string, expiry time.Duration) (string, error) {
	if method != http.MethodGet && method != http.MethodPut {
		return "", fmt.Errorf("objectstore: presign method %q unsupported", method)
	}
	if err := validateKey(key); err != nil {
		return "", err
	}
	if expiry <= 0 || expiry > 7*24*time.Hour {
		return "", fmt.Errorf("objectstore: presign expiry %s out of range", expiry)
	}
	u := s.objectURL(key)
	return s.sign.presignURL(method, u, u.Host, s.now(), expiry), nil
}

func metaFromResponse(key string, resp *http.Response) Meta {
	meta := Meta{
		Key:         key,
		Size:        resp.ContentLength,
		ETag:        strings.Trim(resp.Header.Get("ETag"), `"`),
		ContentType: resp.Header.Get("Content-Type"),
	}
	if ts, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		meta.LastModified = ts
	}
	return meta
}

func httpError(op, key string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	detail := strings.TrimSpace(string(body))
	if detail != "" {
		detail = ": " + detail
	}
	return fmt.Errorf("objectstore: %s %s: %s%s", op, key, resp.Status, detail)
}

func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
	_ = body.Close()
}

func hostOf(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil {
		return u.Hostname()
	}
	return ""
}
