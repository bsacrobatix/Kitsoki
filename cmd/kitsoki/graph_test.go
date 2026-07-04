package main

import (
	"bytes"
	"testing"
)

func runGraphLintCmd(t *testing.T, path string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"graph", "lint", path})
	err := root.Execute()
	return stdout.String(), err
}

func TestGraphLintCmd_SeedCatalogClean(t *testing.T) {
	out, err := runGraphLintCmd(t, "../../docs/proposals/project-object-graph/seed-objects.yaml")
	if err != nil {
		t.Fatalf("graph lint on the seed catalog must exit 0, got: %v\n%s", err, out)
	}
}

func TestGraphLintCmd_BadFixtureFails(t *testing.T) {
	out, err := runGraphLintCmd(t, "../../internal/graph/testdata/lint/cycle.yaml")
	if err == nil {
		t.Fatalf("graph lint on a cyclic catalog must exit non-zero; output:\n%s", out)
	}
}
