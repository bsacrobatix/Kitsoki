package storydemo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const storeSchema = "kitsoki/story-demo-reference/v1"

// FileEvidenceStore provides restart-durable, content-addressed app refs.
type FileEvidenceStore struct {
	Dir   string
	Scope string
}

type storedReference struct {
	Schema        string          `json:"schema"`
	Scope         string          `json:"scope"`
	Kind          string          `json:"kind"`
	Digest        string          `json:"digest"`
	PayloadDigest string          `json:"payload_digest"`
	Payload       json.RawMessage `json:"payload"`
	RecordedAt    string          `json:"recorded_at"`
}

// ScopeID derives an opaque stable scope from the bound app identity and root.
func ScopeID(appID, root string) string {
	sum := sha256.Sum256([]byte(appID + "\x00" + filepath.Clean(root)))
	return hex.EncodeToString(sum[:8])
}

func semanticDigest(kind string, payload []byte) string {
	sum := sha256.Sum256(append(append([]byte(kind), 0), payload...))
	return hex.EncodeToString(sum[:])
}

func (s FileEvidenceStore) Find(ctx context.Context, kind, digest string) (string, json.RawMessage, bool, error) {
	ref := s.reference(kind, digest)
	raw, err := s.Resolve(ctx, ref, kind)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, false, nil
		}
		return "", nil, false, err
	}
	return ref, raw, true, nil
}

func (s FileEvidenceStore) Put(ctx context.Context, kind, digest string, payload json.RawMessage, now time.Time) (string, error) {
	if err := s.validate(kind, digest); err != nil {
		return "", err
	}
	if len(payload) > maxPayloadBytes {
		return "", fmt.Errorf("story demo reference payload is %d bytes, exceeds %d; refusing to truncate", len(payload), maxPayloadBytes)
	}
	if _, _, ok, err := s.Find(ctx, kind, digest); err != nil {
		return "", err
	} else if ok {
		return s.reference(kind, digest), nil
	}
	record := storedReference{
		Schema: storeSchema, Scope: s.Scope, Kind: kind, Digest: digest,
		PayloadDigest: payloadDigest(payload), Payload: payload,
		RecordedAt: now.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	if len(raw) > maxPayloadBytes {
		return "", fmt.Errorf("story demo reference record is %d bytes, exceeds %d; refusing to truncate", len(raw), maxPayloadBytes)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	path := s.path(kind, digest)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".story-demo-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", err
	}
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Link(tmp, path); err != nil {
		if os.IsExist(err) {
			if _, _, ok, getErr := s.Find(ctx, kind, digest); getErr != nil {
				return "", getErr
			} else if ok {
				return s.reference(kind, digest), nil
			}
		}
		return "", err
	}
	return s.reference(kind, digest), nil
}

func (s FileEvidenceStore) Resolve(_ context.Context, ref, wantKind string) (json.RawMessage, error) {
	prefix := "kitsoki://story-demo/" + s.Scope + "/"
	if !strings.HasPrefix(ref, prefix) {
		return nil, fmt.Errorf("reference is outside the bound application scope")
	}
	rest := strings.TrimPrefix(ref, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "sha256" {
		return nil, fmt.Errorf("invalid story demo reference")
	}
	kind, digest := parts[0], parts[2]
	if kind != wantKind {
		return nil, fmt.Errorf("reference kind %q does not match %q", kind, wantKind)
	}
	if err := s.validate(kind, digest); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path(kind, digest))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPayloadBytes {
		return nil, fmt.Errorf("stored story demo reference exceeds %d bytes", maxPayloadBytes)
	}
	var record storedReference
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode story demo reference: %w", err)
	}
	if record.Schema != storeSchema || record.Scope != s.Scope || record.Kind != kind || record.Digest != digest {
		return nil, fmt.Errorf("stored story demo reference identity mismatch")
	}
	if record.PayloadDigest != payloadDigest(record.Payload) {
		return nil, fmt.Errorf("stored story demo reference payload digest mismatch")
	}
	return record.Payload, nil
}

func (s FileEvidenceStore) validate(kind, digest string) error {
	if strings.TrimSpace(s.Dir) == "" || strings.TrimSpace(s.Scope) == "" {
		return fmt.Errorf("story demo reference store is unavailable")
	}
	if kind == "" || strings.ContainsAny(kind, "/\\.") {
		return fmt.Errorf("invalid story demo reference kind")
	}
	if len(digest) != sha256.Size*2 {
		return fmt.Errorf("invalid story demo reference digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("invalid story demo reference digest")
	}
	return nil
}

func (s FileEvidenceStore) path(kind, digest string) string {
	return filepath.Join(s.Dir, s.Scope, kind, digest+".json")
}

func (s FileEvidenceStore) reference(kind, digest string) string {
	return "kitsoki://story-demo/" + s.Scope + "/" + kind + "/sha256/" + digest
}

func payloadDigest(payload []byte) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err == nil {
		payload = compact.Bytes()
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
