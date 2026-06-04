// Package webconfig resolves where the multi-story web UI looks for stories
// and walks those directories to discover one StoryMeta per app.yaml found.
//
// Two concerns live here, deliberately small and dependency-free beyond the
// app loader:
//
//  1. Configuration — WebConfig carries the operator's story_dirs. It loads
//     from a `.kitsoki.yaml` file (gopkg.in/yaml.v3) in the working directory.
//     Resolve applies the precedence flags > .kitsoki.yaml > ./stories default.
//
//  2. Discovery — DiscoverStories walks the resolved directories, matches files
//     literally named `app.yaml`, and loads each via app.Load. A malformed
//     manifest is logged and skipped so one broken story never hides its valid
//     siblings; only an unreadable root directory aborts the walk.
//
// Non-goals (decided leans for the PoC, see docs/proposals/web-multi-story.md):
//   - No fsnotify watch — rescanning is explicit (call DiscoverStories again).
//   - No mode/addr/db config keys — the config file carries only story_dirs.
//     It is intentionally extensible later; for now anything else is ignored.
package webconfig

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/app"
)

// DefaultConfigFile is the file name Load looks for in the working directory.
const DefaultConfigFile = ".kitsoki.yaml"

// defaultStoryDir is the resolution fallback when neither flags nor the config
// file supply a story directory.
var defaultStoryDirs = []string{"./stories"}

// WebConfig is the on-disk configuration for the web UI. It carries only
// story_dirs for now; the struct is the stable extension point for future keys.
type WebConfig struct {
	// StoryDirs lists the directories DiscoverStories walks for app.yaml files.
	StoryDirs []string `yaml:"story_dirs"`
}

// Load reads WebConfig from the given path. A missing file is not an error —
// it returns a zero WebConfig (empty StoryDirs) so the caller can fall back to
// the default via Resolve. Any other read or parse failure is returned.
func Load(path string) (WebConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return WebConfig{}, nil
		}
		return WebConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg WebConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return WebConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Resolve picks the effective story directories with first-non-empty-wins
// precedence: explicit flags (typically from repeatable --stories-dir), then
// the config's StoryDirs, then the ./stories default. The returned slice is a
// fresh copy the caller may retain and mutate.
func Resolve(flagDirs []string, cfg WebConfig) []string {
	switch {
	case len(flagDirs) > 0:
		return append([]string(nil), flagDirs...)
	case len(cfg.StoryDirs) > 0:
		return append([]string(nil), cfg.StoryDirs...)
	default:
		return append([]string(nil), defaultStoryDirs...)
	}
}

// StoryMeta describes one discovered story. Path is the ABSOLUTE path to its
// app.yaml — the canonical session key per the epic's Shared decision #1; the
// app.id (Def.App.ID) is display-only and may collide across stories.
type StoryMeta struct {
	// Path is the absolute path to the story's app.yaml.
	Path string
	// Def is the loaded, validated app definition.
	Def *app.AppDef
}

// DiscoverStories walks each directory recursively, finds every file literally
// named `app.yaml`, and loads it via app.Load. Each successful load yields one
// StoryMeta whose Path is the absolute app.yaml path. A per-file load error is
// logged via the standard logger and skipped — the walk continues so a single
// malformed manifest never suppresses its valid siblings. The only error
// returned is for a root directory that cannot be walked (e.g. unreadable).
func DiscoverStories(dirs []string) ([]StoryMeta, error) {
	var metas []StoryMeta
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// Surface the failure to open the root dir; for entries below it,
				// WalkDir would have already descended, so this is effectively the
				// root-unreadable case the contract aborts on.
				return err
			}
			if d.IsDir() || d.Name() != "app.yaml" {
				return nil
			}
			abs, absErr := filepath.Abs(path)
			if absErr != nil {
				abs = path
			}
			def, loadErr := app.Load(abs)
			if loadErr != nil {
				slog.Warn("webconfig: skipping malformed story", "path", abs, "err", loadErr)
				return nil
			}
			metas = append(metas, StoryMeta{Path: abs, Def: def})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("discover stories under %s: %w", dir, err)
		}
	}
	return metas, nil
}
