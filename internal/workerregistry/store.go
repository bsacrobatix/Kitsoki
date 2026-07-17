package workerregistry

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// localFileShape is the round-trip document Mutate reads and writes. Using
// yaml.Node (rather than fileShape) preserves every other top-level key in
// .kitsoki.local.yaml verbatim — agent_launch_policy, daemon_federation,
// harness_profiles, etc. — so CLI worker mutations never clobber unrelated
// machine-local config. Comment preservation is not attempted (not required
// per the standing-autonomy proposal); key order and content are.
type localFileShape struct {
	root *yaml.Node // DocumentNode
	doc  *yaml.Node // top-level MappingNode (root.Content[0]), created if absent
}

func readLocalDocument(path string) (*localFileShape, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		raw = nil
	}
	root := &yaml.Node{}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, root); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if root.Kind == 0 {
		root.Kind = yaml.DocumentNode
	}
	var doc *yaml.Node
	if len(root.Content) > 0 && root.Content[0].Kind == yaml.MappingNode {
		doc = root.Content[0]
	} else {
		doc = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = []*yaml.Node{doc}
	}
	return &localFileShape{root: root, doc: doc}, nil
}

// workersNode returns the sequence node for the top-level `workers:` key,
// creating an empty one (and the key) if absent.
func (f *localFileShape) workersNode() *yaml.Node {
	for i := 0; i+1 < len(f.doc.Content); i += 2 {
		if f.doc.Content[i].Value == "workers" {
			return f.doc.Content[i+1]
		}
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Value: "workers"}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	f.doc.Content = append(f.doc.Content, key, seq)
	return seq
}

func (f *localFileShape) entries() ([]Entry, error) {
	seq := f.workersNode()
	var raw struct {
		Workers []Entry `yaml:"workers"`
	}
	tmp := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "workers"}, seq,
	}}
	if err := tmp.Decode(&raw); err != nil {
		return nil, err
	}
	return raw.Workers, nil
}

func (f *localFileShape) setEntries(entries []Entry) error {
	seq := f.workersNode()
	node := &yaml.Node{}
	if err := node.Encode(entries); err != nil {
		return err
	}
	seq.Kind = node.Kind
	seq.Tag = node.Tag
	seq.Content = node.Content
	seq.Style = 0
	return nil
}

func writeLocalDocument(path string, f *localFileShape) error {
	out, err := yaml.Marshal(f.root)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".workers-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename into %s: %w", path, err)
	}
	return nil
}

// Mutate loads localConfigPath's `workers:` block, applies fn to the decoded
// entry list, validates the result, and atomically (temp file + rename)
// rewrites the file with every other top-level key preserved. fn returning an
// error aborts without writing.
func Mutate(localConfigPath string, fn func([]Entry) ([]Entry, error)) ([]Entry, error) {
	doc, err := readLocalDocument(localConfigPath)
	if err != nil {
		return nil, err
	}
	current, err := doc.entries()
	if err != nil {
		return nil, fmt.Errorf("decode workers: %w", err)
	}
	next, err := fn(current)
	if err != nil {
		return nil, err
	}
	cfg := Config{Workers: next}
	if err := cfg.Validate(localConfigPath); err != nil {
		return nil, err
	}
	if err := doc.setEntries(next); err != nil {
		return nil, fmt.Errorf("encode workers: %w", err)
	}
	if err := writeLocalDocument(localConfigPath, doc); err != nil {
		return nil, err
	}
	return next, nil
}

// Add appends a new worker entry. It errors if the id already exists.
func Add(localConfigPath string, entry Entry) ([]Entry, error) {
	return Mutate(localConfigPath, func(entries []Entry) ([]Entry, error) {
		for _, e := range entries {
			if e.ID == entry.ID {
				return nil, fmt.Errorf("worker %q already exists", entry.ID)
			}
		}
		return append(entries, entry), nil
	})
}

// Remove deletes the worker entry with the given id. It errors if not found.
func Remove(localConfigPath, id string) ([]Entry, error) {
	return Mutate(localConfigPath, func(entries []Entry) ([]Entry, error) {
		out := make([]Entry, 0, len(entries))
		found := false
		for _, e := range entries {
			if e.ID == id {
				found = true
				continue
			}
			out = append(out, e)
		}
		if !found {
			return nil, fmt.Errorf("worker %q not found", id)
		}
		return out, nil
	})
}

// SetEnabled flips the enabled bit for one worker id. It errors if not found.
func SetEnabled(localConfigPath, id string, enabled bool) ([]Entry, error) {
	return Mutate(localConfigPath, func(entries []Entry) ([]Entry, error) {
		found := false
		for i := range entries {
			if entries[i].ID == id {
				entries[i].Enabled = enabled
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("worker %q not found", id)
		}
		return entries, nil
	})
}
