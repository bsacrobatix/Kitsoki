package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"kitsoki/internal/capsuletest"
)

func TestGitSourceProviderClonesSelfAndPinnedCapsuleCommit(t *testing.T) {
	project := capsuletest.Open(t, "clean-repo")
	head, err := runGit(context.Background(), project, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	provider := GitSourceProvider{ProjectRoot: project}
	self, err := provider.Create(context.Background(), Definition{ID: "self", Source: Source{Kind: SourceSelf}}, Instance{ID: "self", Path: filepath.Join(t.TempDir(), "self")})
	if err != nil {
		t.Fatal(err)
	}
	if self.Head != head[:len(head)-1] {
		t.Fatalf("self head=%q want=%q", self.Head, head)
	}
	if err := provider.Close(context.Background(), Instance{Path: self.Path}); err != nil {
		t.Fatal(err)
	}
	pinned, err := provider.Create(context.Background(), Definition{ID: "pinned", Source: Source{Kind: SourcePinned, Ref: project, Commit: selfHead(head)}}, Instance{ID: "pinned", Path: filepath.Join(t.TempDir(), "pinned")})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Head != selfHead(head) {
		t.Fatalf("pinned=%q want=%q", pinned.Head, head)
	}
	if _, err := os.Stat(filepath.Join(pinned.Path, instanceSentinel)); err != nil {
		t.Fatal(err)
	}
}
func selfHead(v string) string {
	for len(v) > 0 && (v[len(v)-1] == '\n' || v[len(v)-1] == '\r') {
		v = v[:len(v)-1]
	}
	return v
}
