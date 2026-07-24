package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"

	"kitsoki/internal/atomicfile"
)

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{records: map[string]Record{}} }
func (s *MemoryStore) Create(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[r.ID]; ok {
		return fmt.Errorf("capsule runtime: record %q exists", r.ID)
	}
	s.records[r.ID] = r
	return nil
}
func (s *MemoryStore) Get(_ context.Context, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return r, nil
}
func (s *MemoryStore) List(_ context.Context) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *MemoryStore) Update(_ context.Context, id string, fn func(*Record) error) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err := fn(&r); err != nil {
		return Record{}, err
	}
	s.records[id] = r
	return r, nil
}

// FileStore is the local durable control-plane store. Each update is written
// through a same-directory temporary file and rename, so a receipt is never
// partially visible after a host interruption. Multi-process coordination is
// deliberately delegated to the daemon-backed store in a later control-plane
// slice; this store is for one local runtime manager.
type FileStore struct {
	mu   sync.Mutex
	path string
}

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (s *FileStore) Create(ctx context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := records[r.ID]; ok {
		return fmt.Errorf("capsule runtime: record %q exists", r.ID)
	}
	records[r.ID] = r
	return s.save(records)
}
func (s *FileStore) Get(ctx context.Context, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.load()
	if err != nil {
		return Record{}, err
	}
	r, ok := records[id]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return r, nil
}
func (s *FileStore) List(ctx context.Context) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(records))
	for _, r := range records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *FileStore) Update(ctx context.Context, id string, fn func(*Record) error) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.load()
	if err != nil {
		return Record{}, err
	}
	r, ok := records[id]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err := fn(&r); err != nil {
		return Record{}, err
	}
	records[id] = r
	if err := s.save(records); err != nil {
		return Record{}, err
	}
	return r, nil
}
func (s *FileStore) load() (map[string]Record, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("capsule runtime: read records: %w", err)
	}
	records := map[string]Record{}
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("capsule runtime: decode records: %w", err)
	}
	return records, nil
}
func (s *FileStore) save(records map[string]Record) error {
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.path, raw, 0o600, 0o755)
}
