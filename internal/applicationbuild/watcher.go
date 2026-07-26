package applicationbuild

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SourceWatcher interface {
	Watch(context.Context, string, func(string) error) error
}

type PollingWatcher struct {
	interval time.Duration
}

func NewPollingWatcher(interval time.Duration) PollingWatcher {
	if interval <= 0 {
		interval = 300 * time.Millisecond
	}
	return PollingWatcher{interval: interval}
}

func (w PollingWatcher) Watch(ctx context.Context, root string, changed func(string) error) error {
	previous, err := sourceSnapshot(root)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			next, err := sourceSnapshot(root)
			if err != nil {
				return err
			}
			for _, path := range changedSources(previous, next) {
				if err := changed(path); err != nil {
					return err
				}
			}
			previous = next
		}
	}
}

type sourceStamp struct {
	size    int64
	modTime int64
}

func sourceSnapshot(root string) (map[string]sourceStamp, error) {
	out := make(map[string]sourceStamp)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == "node_modules" || name == ".git" || name == ".temp" || name == ".artifacts" {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml", ".json", ".star", ".vue", ".ts", ".tsx", ".js", ".css":
		default:
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out[path] = sourceStamp{size: info.Size(), modTime: info.ModTime().UnixNano()}
		return nil
	})
	return out, err
}

func changedSources(previous, next map[string]sourceStamp) []string {
	set := make(map[string]struct{})
	for path, stamp := range next {
		if prior, ok := previous[path]; !ok || prior != stamp {
			set[path] = struct{}{}
		}
	}
	for path := range previous {
		if _, ok := next[path]; !ok {
			set[path] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for path := range set {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
