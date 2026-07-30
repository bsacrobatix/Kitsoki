package appclosure

import "testing"

const testSchema = "appclosure-test/v1"

func TestDigestStableAcrossMapIterationOrder(t *testing.T) {
	// Go map iteration order is randomized per-process; recomputing the same
	// logical file set (built in a different literal order) must still
	// produce the identical digest, proving the sort inside Digest — not
	// insertion/iteration order — determines the byte layout.
	a := map[string]File{
		"b.yaml": {Mode: 0o644, Bytes: []byte("b")},
		"a.yaml": {Mode: 0o644, Bytes: []byte("a")},
		"c.yaml": {Mode: 0o644, Bytes: []byte("c")},
	}
	b := map[string]File{
		"c.yaml": {Mode: 0o644, Bytes: []byte("c")},
		"a.yaml": {Mode: 0o644, Bytes: []byte("a")},
		"b.yaml": {Mode: 0o644, Bytes: []byte("b")},
	}
	got1 := Digest(testSchema, a)
	got2 := Digest(testSchema, b)
	if got1 != got2 {
		t.Fatalf("digest depends on map iteration order: %s vs %s", got1, got2)
	}
}

func TestDigestChangesOnOneByteContentChange(t *testing.T) {
	base := map[string]File{"room.yaml": {Mode: 0o644, Bytes: []byte("state: idle\n")}}
	changed := map[string]File{"room.yaml": {Mode: 0o644, Bytes: []byte("state: idlf\n")}}
	if Digest(testSchema, base) == Digest(testSchema, changed) {
		t.Fatal("one-byte content change did not change digest")
	}
}

func TestDigestChangesOnPathRename(t *testing.T) {
	base := map[string]File{"room.yaml": {Mode: 0o644, Bytes: []byte("state: idle\n")}}
	renamed := map[string]File{"rooms/idle.yaml": {Mode: 0o644, Bytes: []byte("state: idle\n")}}
	if Digest(testSchema, base) == Digest(testSchema, renamed) {
		t.Fatal("path rename did not change digest")
	}
}

func TestDigestChangesOnModeChange(t *testing.T) {
	base := map[string]File{"script.star": {Mode: 0o644, Bytes: []byte("def main(): pass\n")}}
	executable := map[string]File{"script.star": {Mode: 0o755, Bytes: []byte("def main(): pass\n")}}
	if Digest(testSchema, base) == Digest(testSchema, executable) {
		t.Fatal("mode-only change did not change digest")
	}
}

func TestDigestDiffersBySchemaForIdenticalFiles(t *testing.T) {
	files := map[string]File{"app.yaml": {Mode: 0o644, Bytes: []byte("app: {}\n")}}
	got1 := Digest("schema-one/v1", files)
	got2 := Digest("schema-two/v1", files)
	if got1 == got2 {
		t.Fatal("different schema values produced identical digests for identical files")
	}
}

func TestDigestEmptyFileSetIsStableAndDiffersFromNonEmpty(t *testing.T) {
	empty := map[string]File{}
	got1 := Digest(testSchema, empty)
	got2 := Digest(testSchema, map[string]File{})
	if got1 != got2 {
		t.Fatalf("empty file set digest not stable: %s vs %s", got1, got2)
	}
	if got1 == "" {
		t.Fatal("empty file set produced empty digest")
	}
	nonEmpty := map[string]File{"app.yaml": {Mode: 0o644, Bytes: []byte("app: {}\n")}}
	if got1 == Digest(testSchema, nonEmpty) {
		t.Fatal("empty and non-empty file sets produced identical digests")
	}
}

func TestDigestBytesStampsDefaultMode(t *testing.T) {
	// DigestBytes(schema, map[string][]byte) must be exactly Digest with
	// DefaultMode stamped on every entry — not some independent layout.
	raw := map[string][]byte{"app.yaml": []byte("app: {}\n")}
	withMode := map[string]File{"app.yaml": {Mode: DefaultMode, Bytes: []byte("app: {}\n")}}
	if DigestBytes(testSchema, raw) != Digest(testSchema, withMode) {
		t.Fatal("DigestBytes did not stamp DefaultMode consistently with Digest")
	}
}
