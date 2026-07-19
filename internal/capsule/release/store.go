package release

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileStore is an append-only local manifest store. The digest is part of the
// filename, so a caller cannot overwrite a different candidate by reusing an ID.
type FileStore struct{ Root string }

func (s FileStore) Put(_ context.Context, c Candidate, digest string) error {
	if s.Root == "" {
		return fmt.Errorf("release: manifest store root is required")
	}
	if got, err := Digest(c); err != nil {
		return err
	} else if got != digest {
		return fmt.Errorf("release: candidate digest mismatch")
	}
	if err := os.MkdirAll(s.Root, 0755); err != nil {
		return err
	}
	path := filepath.Join(s.Root, digest+".json")
	if old, err := os.ReadFile(path); err == nil {
		var existing Candidate
		if json.Unmarshal(old, &existing) == nil && existing.ID == c.ID {
			return nil
		}
		return fmt.Errorf("release: immutable manifest already exists with different content: %s", digest)
	} else if !os.IsNotExist(err) {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
func (s FileStore) Get(_ context.Context, digest string) (Candidate, error) {
	b, err := os.ReadFile(filepath.Join(s.Root, digest+".json"))
	if err != nil {
		return Candidate{}, err
	}
	var c Candidate
	if err := json.Unmarshal(b, &c); err != nil {
		return Candidate{}, err
	}
	if got, err := Digest(c); err != nil || got != digest {
		return Candidate{}, fmt.Errorf("release: stored manifest digest mismatch: %s", digest)
	}
	return c, nil
}
