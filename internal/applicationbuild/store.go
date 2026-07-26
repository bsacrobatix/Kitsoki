package applicationbuild

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// InstalledBundle is one validated content-addressed production bundle.
type InstalledBundle struct {
	Manifest Manifest
	Dir      string
}

// Latest resolves the newest valid manifest for applicationID. It recomputes
// both the declared asset digest and the final identity-bearing bundle digest;
// invalid, tampered, or partial directories are ignored.
func Latest(root, applicationID string) (InstalledBundle, error) {
	appPart := sanitizePathPart(applicationID)
	if appPart == "" || appPart != applicationID {
		return InstalledBundle{}, fmt.Errorf("application bundle: invalid application id %q", applicationID)
	}
	appRoot := filepath.Join(root, appPart)
	entries, err := os.ReadDir(appRoot)
	if err != nil {
		return InstalledBundle{}, fmt.Errorf("application bundle: list %q: %w", applicationID, err)
	}
	var candidates []InstalledBundle
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(appRoot, entry.Name())
		manifestPath := filepath.Join(dir, "application-manifest.json")
		manifestInfo, infoErr := os.Lstat(manifestPath)
		if infoErr != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		raw, readErr := os.ReadFile(manifestPath)
		if readErr != nil {
			continue
		}
		var manifest Manifest
		if json.Unmarshal(raw, &manifest) != nil ||
			manifest.Schema != ManifestSchema ||
			manifest.ApplicationID != applicationID ||
			manifest.Digest != "sha256:"+entry.Name() ||
			manifest.CreatedAt.IsZero() ||
			manifest.Entry == "" ||
			!validManifestFiles(manifest.Files, manifest.Entry) {
			continue
		}
		manifest.ArtifactDir = dir
		bundle := InstalledBundle{Manifest: manifest, Dir: dir}
		valid := true
		for _, file := range manifest.Files {
			if _, assetErr := bundle.Asset(file); assetErr != nil {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		outputDigest, files, digestErr := digestDeclaredFiles(dir, manifest.Files)
		if digestErr != nil || !reflect.DeepEqual(files, manifest.Files) {
			continue
		}
		identity, identityErr := bundleIdentityDigest(
			outputDigest,
			manifest.Entry,
			manifest.Components,
			manifest.Theme,
			manifest.Native,
			manifest.Compatibility,
		)
		if identityErr == nil && identity == manifest.Digest {
			candidates = append(candidates, bundle)
		}
	}
	if len(candidates) == 0 {
		return InstalledBundle{}, fmt.Errorf("application bundle: no valid build for %q", applicationID)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Manifest.CreatedAt.Equal(candidates[j].Manifest.CreatedAt) {
			return candidates[i].Manifest.Digest > candidates[j].Manifest.Digest
		}
		return candidates[i].Manifest.CreatedAt.After(candidates[j].Manifest.CreatedAt)
	})
	return candidates[0], nil
}

func validManifestFiles(values []string, entry string) bool {
	if len(values) == 0 || !sort.StringsAreSorted(values) {
		return false
	}
	foundEntry := false
	for index, value := range values {
		if value == "" ||
			path.Clean("/" + value)[1:] != value ||
			value == "application-manifest.json" ||
			(index > 0 && value == values[index-1]) {
			return false
		}
		if value == entry {
			foundEntry = true
		}
	}
	return foundEntry
}

// Asset resolves only files declared by the selected manifest (plus the
// manifest itself), rejecting traversal, symlinks, and stale undeclared files.
func (b InstalledBundle) Asset(name string) (string, error) {
	name = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(name)), "/")
	if name == "" {
		name = b.Manifest.Entry
	}
	if name == "application-manifest.json" {
		return filepath.Join(b.Dir, name), nil
	}
	declared := false
	for _, file := range b.Manifest.Files {
		if file == name {
			declared = true
			break
		}
	}
	if !declared {
		return "", fmt.Errorf("application bundle: asset %q is not declared", name)
	}
	candidate := filepath.Join(b.Dir, filepath.FromSlash(name))
	relative, err := filepath.Rel(b.Dir, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("application bundle: asset escapes bundle root")
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("application bundle: asset %q is not a regular file", name)
	}
	return candidate, nil
}
