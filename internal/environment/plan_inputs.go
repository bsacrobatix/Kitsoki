package environment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PlanInputReader is the read-only native seam a planner needs before it can
// create a provider action. It has no cloud authority.
type PlanInputReader interface {
	PlanInputs(context.Context) (PlanInputs, error)
}

type PlanInputs struct {
	ProfileDocuments []ProfileDocument
	Integrity        Outcome
	ManifestDigest   string
	Observations     Observations
}

type ProfileDocument struct {
	Source   ProfileSource
	Document map[string]any
}

type ProfileSource struct {
	Path   string
	Digest string
}

type Observations struct {
	DNS        DNSObservation
	Deployment DeploymentObservation
	Migration  MigrationObservation
}

type DNSObservation struct{ ResolvedIPs []string }
type DeploymentObservation struct{ Healthy, Current bool }
type MigrationObservation struct{ SourceConfigured bool }

// ProfileBundleLoader dynamically discovers JSON profiles beneath ProfileRoot,
// verifies every discovered document against SHA256SUMS, and returns sorted
// documents. ProfileRoot and DigestFile are operator configuration fixed at
// construction; Story input cannot redirect filesystem access.
type ProfileBundleLoader struct {
	ProfileRoot  string
	DigestFile   string
	Observations Observations
	FS           fs.FS
}

func (l ProfileBundleLoader) PlanInputs(_ context.Context) (PlanInputs, error) {
	filesystem := l.FS
	if filesystem == nil {
		filesystem = os.DirFS("/")
	}
	root := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(l.ProfileRoot)), "/")
	if root == "." || root == "" {
		return PlanInputs{Integrity: Blocked(ReasonInvalidRequest, "profile root is required", "configure a fixed profile root")}, nil
	}
	entries, err := fs.ReadDir(filesystem, root)
	if err != nil {
		return PlanInputs{}, fmt.Errorf("read environment profile root: %w", err)
	}
	digestFile := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(l.DigestFile)), "/")
	digests, manifestDigest, err := readDigests(filesystem, digestFile)
	if err != nil {
		return PlanInputs{Integrity: Blocked(ReasonInvalidRequest, "profile integrity manifest is unavailable", "provide SHA256SUMS for the configured profile bundle", Evidence{Kind: "integrity_manifest", Ref: l.DigestFile, Detail: err.Error()})}, nil
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return PlanInputs{Integrity: Blocked(ReasonNotFound, "environment profile discovery found no JSON profiles", "add a profile document under the configured profile root", Evidence{Kind: "profile_root", Ref: l.ProfileRoot})}, nil
	}
	inputs := PlanInputs{Observations: l.Observations, ManifestDigest: manifestDigest}
	for _, name := range names {
		path := filepath.ToSlash(filepath.Join(root, name))
		contents, readErr := fs.ReadFile(filesystem, path)
		if readErr != nil {
			return PlanInputs{}, fmt.Errorf("read profile %s: %w", path, readErr)
		}
		digest := sha256.Sum256(contents)
		digestText := hex.EncodeToString(digest[:])
		want, covered := digests[path]
		if !covered || !strings.EqualFold(want, digestText) {
			return PlanInputs{Integrity: Blocked(ReasonAttributeMismatch, "environment profile bundle integrity check failed", "regenerate SHA256SUMS from the dynamically discovered profile tree", Evidence{Kind: "profile", Ref: path, Detail: digestText})}, nil
		}
		var document map[string]any
		if err := json.Unmarshal(contents, &document); err != nil {
			return PlanInputs{Integrity: Blocked(ReasonInvalidRequest, "environment profile is not a JSON object", "repair the profile document", Evidence{Kind: "profile", Ref: path, Detail: err.Error()})}, nil
		}
		if document == nil {
			return PlanInputs{Integrity: Blocked(ReasonInvalidRequest, "environment profile must be a JSON object", "repair the profile document", Evidence{Kind: "profile", Ref: path})}, nil
		}
		inputs.ProfileDocuments = append(inputs.ProfileDocuments, ProfileDocument{Source: ProfileSource{Path: path, Digest: digestText}, Document: document})
	}
	for path := range digests {
		if strings.HasPrefix(path, root+"/") && strings.HasSuffix(path, ".json") && !containsProfile(inputs.ProfileDocuments, path) {
			return PlanInputs{Integrity: Blocked(ReasonAttributeMismatch, "integrity manifest names a profile absent from discovery", "regenerate SHA256SUMS from the dynamically discovered profile tree", Evidence{Kind: "profile", Ref: path})}, nil
		}
	}
	inputs.Integrity = Passed(Evidence{Kind: "profile_bundle", Ref: l.ProfileRoot, Detail: fmt.Sprintf("%d profiles verified", len(inputs.ProfileDocuments))})
	return inputs, nil
}

func readDigests(filesystem fs.FS, path string) (map[string]string, string, error) {
	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		return nil, "", err
	}
	manifest := sha256.Sum256(contents)
	manifestDigest := "sha256:" + hex.EncodeToString(manifest[:])
	result := make(map[string]string)
	for lineNumber, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			return nil, "", fmt.Errorf("line %d is not a sha256sum entry", lineNumber+1)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return nil, "", fmt.Errorf("line %d has an invalid digest", lineNumber+1)
		}
		path := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(strings.TrimPrefix(fields[1], "*"))), "./")
		if path == "" || strings.HasPrefix(path, "../") || filepath.IsAbs(path) {
			return nil, "", fmt.Errorf("line %d has an unsafe path", lineNumber+1)
		}
		if _, exists := result[path]; exists {
			return nil, "", fmt.Errorf("line %d repeats %q", lineNumber+1, path)
		}
		result[path] = strings.ToLower(fields[0])
	}
	return result, manifestDigest, nil
}

func containsProfile(documents []ProfileDocument, path string) bool {
	for _, document := range documents {
		if document.Source.Path == path {
			return true
		}
	}
	return false
}
