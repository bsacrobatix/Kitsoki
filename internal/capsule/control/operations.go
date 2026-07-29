package control

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// FileEntry is a path-only directory listing. Paths are always relative to the
// workspace and never reveal verifier overlays or a machine-local root.
type FileEntry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Symlink   bool   `json:"symlink,omitempty"`
	Size      int64  `json:"size,omitempty"`
}
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}
type SearchResult struct {
	Matches       []SearchMatch `json:"matches"`
	FilesExamined int           `json:"files_examined"`
	BytesExamined int64         `json:"bytes_examined"`
	SkippedFiles  int           `json:"skipped_files,omitempty"`
	Truncated     bool          `json:"truncated,omitempty"`
}

const (
	readMaxFileBytes      = 4 << 20
	writeMaxFileBytes     = 4 << 20
	commandMaxOutputBytes = 1 << 20
	searchDefaultMatches  = 100
	searchMaxMatches      = 500
	searchMaxEntries      = 20_000
	searchMaxFiles        = 10_000
	searchMaxBytes        = 16 << 20
	searchMaxFileBytes    = 1 << 20
	searchMaxLineBytes    = 4 << 10
)

var ignoredSearchDirectories = map[string]bool{
	".git":              true,
	".kitsoki-verifier": true,
	".cache":            true,
	".gradle":           true,
	".next":             true,
	".venv":             true,
	"__pycache__":       true,
	"build":             true,
	"coverage":          true,
	"dist":              true,
	"node_modules":      true,
	"target":            true,
	"vendor":            true,
	"venv":              true,
}

type CommandResult struct {
	Handle          Handle `json:"workspace"`
	ExitCode        int    `json:"exit_code"`
	Output          string `json:"output"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	TimedOut        bool   `json:"timed_out,omitempty"`
}

// boundedCommandOutput retains a diagnostic prefix while reporting the byte
// count accepted by io.Writer. A noisy project command therefore cannot grow
// the Capsule server or CLI process without bound.
type boundedCommandOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedCommandOutput) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || written > 0
		return written, nil
	}
	if len(p) > remaining {
		_, _ = b.buffer.Write(p[:remaining])
		b.truncated = true
		return written, nil
	}
	_, _ = b.buffer.Write(p)
	return written, nil
}

func (b *boundedCommandOutput) String() string { return b.buffer.String() }

type VCSStatus struct {
	Handle    Handle `json:"workspace"`
	Branch    string `json:"branch"`
	Head      string `json:"head"`
	Dirty     bool   `json:"dirty"`
	Porcelain string `json:"porcelain,omitempty"`
}

// HeadRelation classifies the live git HEAD of a managed workspace against the
// head the Capsule control plane has registered for it.
//
// Agents inside a workspace commit with plain `git commit` — that is the
// natural, supported thing to do — so the registered head routinely falls
// behind the real branch. Capsule reconciles from git rather than demanding
// its own commit verb, but only for the one relation that provably cannot drop
// registered history: a fast-forward.
type HeadRelation string

const (
	// HeadInSync means the registered head is exactly the live git HEAD.
	HeadInSync HeadRelation = "in_sync"
	// HeadAhead means git HEAD is a strict descendant of the registered head:
	// ordinary `git commit` work, safe to adopt.
	HeadAhead HeadRelation = "ahead"
	// HeadBehind means the registered head is a strict descendant of git HEAD:
	// the workspace was reset/rolled back below what Capsule recorded.
	HeadBehind HeadRelation = "behind"
	// HeadDiverged means neither head reaches the other: a rebase, amend, or
	// force-move that would drop registered history if adopted silently.
	HeadDiverged HeadRelation = "diverged"
	// HeadMissing means the registered head commit is not present in the
	// workspace object database at all.
	HeadMissing HeadRelation = "missing"
	// HeadUnregistered means no head was ever registered for this instance.
	// There is no registered history to drop, so adoption is safe.
	HeadUnregistered HeadRelation = "unregistered"
)

// Adoptable reports whether the live git HEAD can replace the registered head
// without discarding any registered commit.
func (r HeadRelation) Adoptable() bool { return r == HeadAhead || r == HeadUnregistered }

// HeadDrift is the operator-facing diagnosis of registered head vs git HEAD.
// It is deliberately printable: a person must be able to tell what state the
// workspace is in, and what to run next, without reading Go source.
type HeadDrift struct {
	Handle         Handle       `json:"workspace"`
	Path           string       `json:"-"`
	Branch         string       `json:"branch,omitempty"`
	RegisteredHead string       `json:"registered_head,omitempty"`
	GitHead        string       `json:"git_head,omitempty"`
	Relation       HeadRelation `json:"relation"`
	Dirty          bool         `json:"dirty"`
	Ahead          int          `json:"ahead,omitempty"`
	Behind         int          `json:"behind,omitempty"`
}

// ErrHeadDiverged marks the one drift case that genuinely needs a human: the
// live branch cannot be adopted without dropping commits Capsule registered.
var ErrHeadDiverged = fmt.Errorf("capsule vcs: workspace head diverged from the registered head")

// ErrNothingToCommit marks a workspace that is already fully reconciled: clean
// tree, registered head equal to git HEAD. It is not a failure of the caller's
// intent, it is "there is nothing left to do".
var ErrNothingToCommit = fmt.Errorf("capsule vcs: nothing to commit")

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "(unset)"
	}
	return sha
}

// Explain renders the WHAT/WHY/NEXT triple every refusal in this area owes the
// operator.
func (d HeadDrift) Explain() string {
	switch d.Relation {
	case HeadInSync:
		return fmt.Sprintf("workspace %s is in sync: registered head %s equals git HEAD, and the tree is clean", d.Handle.ID, shortSHA(d.RegisteredHead))
	case HeadAhead:
		return fmt.Sprintf("workspace %s advanced via git: registered head %s -> git HEAD %s (%d commit(s) ahead)", d.Handle.ID, shortSHA(d.RegisteredHead), shortSHA(d.GitHead), d.Ahead)
	case HeadUnregistered:
		return fmt.Sprintf("workspace %s has no registered head; git HEAD is %s", d.Handle.ID, shortSHA(d.GitHead))
	case HeadBehind:
		return fmt.Sprintf("workspace %s moved BACKWARDS: git HEAD %s is an ancestor of registered head %s (%d registered commit(s) would be dropped)", d.Handle.ID, shortSHA(d.GitHead), shortSHA(d.RegisteredHead), d.Behind)
	case HeadDiverged:
		return fmt.Sprintf("workspace %s DIVERGED: registered head %s and git HEAD %s share no fast-forward path (%d registered commit(s) would be dropped, %d local commit(s) are unregistered) — this is a rebase, amend, or reset, not ordinary git commit work", d.Handle.ID, shortSHA(d.RegisteredHead), shortSHA(d.GitHead), d.Behind, d.Ahead)
	case HeadMissing:
		return fmt.Sprintf("workspace %s cannot be reconciled: registered head %s is not present in the workspace object database (git HEAD is %s)", d.Handle.ID, shortSHA(d.RegisteredHead), shortSHA(d.GitHead))
	default:
		return fmt.Sprintf("workspace %s head relation is %s", d.Handle.ID, d.Relation)
	}
}

// Next is the exact command an operator should run for this state.
func (d HeadDrift) Next() string {
	switch d.Relation {
	case HeadAhead, HeadUnregistered:
		if d.Dirty {
			return fmt.Sprintf("commit or discard the remaining working-tree changes, then run: kitsoki capsule workspace reconcile --id %s", d.Handle.ID)
		}
		return fmt.Sprintf("run: kitsoki capsule workspace reconcile --id %s", d.Handle.ID)
	case HeadInSync:
		if d.Dirty {
			return fmt.Sprintf("run: kitsoki capsule workspace commit --id %s --message \"<message>\" (or commit with git, then reconcile)", d.Handle.ID)
		}
		return fmt.Sprintf("nothing to reconcile; inspect with: kitsoki capsule workspace status --id %s --json", d.Handle.ID)
	case HeadBehind, HeadDiverged, HeadMissing:
		return fmt.Sprintf("inspect the divergence with: git -C <workspace> log --oneline --left-right %s...%s ; recover the registered commits, or recreate the workspace. Capsule will not adopt a head that drops registered history", shortSHA(d.RegisteredHead), shortSHA(d.GitHead))
	default:
		return fmt.Sprintf("inspect with: kitsoki capsule workspace status --id %s --json", d.Handle.ID)
	}
}

func (d HeadDrift) refusal(action string) error {
	return fmt.Errorf("%w: %s: %s; %s", ErrHeadDiverged, action, d.Explain(), d.Next())
}

// InspectHead diagnoses registered head vs live git HEAD. It performs no
// mutation and requires no effect grant: diagnosis must always be available,
// especially in the states where an action would be refused.
func (m *Manager) InspectHead(ctx context.Context, h Handle) (HeadDrift, error) {
	in, err := m.Status(ctx, h)
	if err != nil {
		return HeadDrift{}, err
	}
	path, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return HeadDrift{}, err
	}
	return m.inspectHeadAt(ctx, Handle{ID: in.ID, Generation: in.Generation}, path, strings.TrimSpace(in.Head))
}

func (m *Manager) inspectHeadAt(ctx context.Context, h Handle, path, registered string) (HeadDrift, error) {
	drift := HeadDrift{Handle: h, Path: path, RegisteredHead: registered}
	head, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return HeadDrift{}, err
	}
	drift.GitHead = strings.TrimSpace(head)
	if branch, err := git(ctx, path, "branch", "--show-current"); err == nil {
		drift.Branch = strings.TrimSpace(branch)
	}
	porcelain, err := git(ctx, path, "status", "--porcelain")
	if err != nil {
		return HeadDrift{}, err
	}
	drift.Dirty = strings.TrimSpace(porcelain) != ""

	switch {
	case registered == "":
		drift.Relation = HeadUnregistered
		return drift, nil
	case registered == drift.GitHead:
		drift.Relation = HeadInSync
		return drift, nil
	}
	if _, err := git(ctx, path, "cat-file", "-e", registered+"^{commit}"); err != nil {
		drift.Relation = HeadMissing
		return drift, nil
	}
	counts, err := git(ctx, path, "rev-list", "--left-right", "--count", registered+"..."+drift.GitHead)
	if err != nil {
		return HeadDrift{}, err
	}
	fields := strings.Fields(counts)
	if len(fields) == 2 {
		fmt.Sscanf(fields[0], "%d", &drift.Behind)
		fmt.Sscanf(fields[1], "%d", &drift.Ahead)
	}
	switch {
	case drift.Behind == 0 && drift.Ahead > 0:
		drift.Relation = HeadAhead
	case drift.Ahead == 0 && drift.Behind > 0:
		drift.Relation = HeadBehind
	default:
		drift.Relation = HeadDiverged
	}
	return drift, nil
}

// AdoptHeadResult reports exactly what AdoptHead did, so callers can say so out
// loud instead of silently mutating registered state.
type AdoptHeadResult struct {
	Handle   Handle    `json:"workspace"`
	Drift    HeadDrift `json:"drift"`
	Adopted  bool      `json:"adopted"`
	Previous string    `json:"previous_head,omitempty"`
	Head     string    `json:"head,omitempty"`
	Commits  int       `json:"commits_adopted,omitempty"`
	Summary  string    `json:"summary"`
}

// AdoptHead reconciles the registered head forward to the live git HEAD.
//
// Safety properties, all preserved and none of them optional:
//   - the working tree must be clean (nothing uncommitted is ever adopted, so
//     the adopted head always describes the full source),
//   - the live HEAD must be a strict descendant of the registered head, so no
//     registered commit can be dropped,
//   - a rewrite (behind/diverged/missing registered commit) fails loudly and
//     names the divergence.
//
// Adoption records provenance only. It never runs a gate and never marks work
// validated: promotion admission still requires its own receipt.
func (m *Manager) AdoptHead(ctx context.Context, h Handle) (AdoptHeadResult, error) {
	if !m.Grant.Allows("effect", "vcs_commit") {
		return AdoptHeadResult{}, fmt.Errorf("%w: vcs_commit", ErrDenied)
	}
	drift, err := m.InspectHead(ctx, h)
	if err != nil {
		return AdoptHeadResult{}, err
	}
	return m.adopt(ctx, drift)
}

func (m *Manager) adopt(ctx context.Context, drift HeadDrift) (AdoptHeadResult, error) {
	if !drift.Relation.Adoptable() {
		if drift.Relation == HeadInSync {
			return AdoptHeadResult{
				Handle:   drift.Handle,
				Drift:    drift,
				Previous: drift.RegisteredHead,
				Head:     drift.GitHead,
				Summary:  drift.Explain(),
			}, nil
		}
		return AdoptHeadResult{}, drift.refusal("refusing to adopt workspace head")
	}
	if drift.Dirty {
		return AdoptHeadResult{}, fmt.Errorf("capsule vcs: refusing to adopt workspace head: %s, but the working tree has uncommitted or untracked changes, so the adopted head would not describe the full source; %s", drift.Explain(), drift.Next())
	}
	handle, err := m.markWith(ctx, drift.Handle, StateCommitted, "capsule.workspace.head_adopted", func(in *Instance) {
		in.Head = drift.GitHead
	})
	if err != nil {
		return AdoptHeadResult{}, err
	}
	return AdoptHeadResult{
		Handle:   handle,
		Drift:    drift,
		Adopted:  true,
		Previous: drift.RegisteredHead,
		Head:     drift.GitHead,
		Commits:  drift.Ahead,
		Summary:  fmt.Sprintf("registered head advanced %s -> %s, %d commit(s) adopted", shortSHA(drift.RegisteredHead), shortSHA(drift.GitHead), drift.Ahead),
	}, nil
}

func (m *Manager) ReadFile(ctx context.Context, h Handle, relative string) ([]byte, error) {
	path, err := m.resolve(ctx, h, relative, true)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("capsule fs: read target must be a regular file")
	}
	if info.Size() > readMaxFileBytes {
		return nil, fmt.Errorf("capsule fs: file exceeds %d byte read limit", readMaxFileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, readMaxFileBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(raw) > readMaxFileBytes {
		return nil, fmt.Errorf("capsule fs: file exceeds %d byte read limit", readMaxFileBytes)
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, fmt.Errorf("capsule fs: binary file reads are not supported")
	}
	return raw, nil
}

func (m *Manager) ListFiles(ctx context.Context, h Handle, relative string) ([]FileEntry, error) {
	root, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return nil, err
	}
	path := ""
	if strings.TrimSpace(relative) == "" || filepath.Clean(relative) == "." {
		path = root
	} else {
		path, err = m.resolve(ctx, h, relative, true)
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".git" || entry.Name() == ".kitsoki-verifier" {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			return nil, e
		}
		rel, e := filepath.Rel(root, filepath.Join(path, entry.Name()))
		if e != nil {
			return nil, e
		}
		out = append(out, FileEntry{Path: filepath.ToSlash(rel), Directory: entry.IsDir(), Symlink: entry.Type()&os.ModeSymlink != 0, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (m *Manager) SearchFiles(ctx context.Context, h Handle, query string, limit int) (SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return SearchResult{}, fmt.Errorf("capsule fs: query is required")
	}
	root, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return SearchResult{}, err
	}
	if limit <= 0 {
		limit = searchDefaultMatches
	}
	if limit > searchMaxMatches {
		limit = searchMaxMatches
	}
	result := SearchResult{Matches: []SearchMatch{}}
	entries := 0
	needle := []byte(query)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > searchMaxEntries {
			result.Truncated = true
			return fs.SkipAll
		}
		if d.IsDir() && ignoredSearchDirectories[d.Name()] {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if result.FilesExamined >= searchMaxFiles {
			result.Truncated = true
			return fs.SkipAll
		}
		result.FilesExamined++
		if d.Type()&os.ModeSymlink != 0 {
			result.SkippedFiles++
			return nil
		}
		info, e := d.Info()
		if e != nil || !info.Mode().IsRegular() || info.Size() > searchMaxFileBytes {
			result.SkippedFiles++
			return nil
		}
		if result.BytesExamined+info.Size() > searchMaxBytes {
			result.Truncated = true
			return fs.SkipAll
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			result.SkippedFiles++
			return nil
		}
		resolved, e := ResolveWorkspacePath(root, rel, true)
		if e != nil {
			result.SkippedFiles++
			return nil
		}
		file, e := os.Open(resolved)
		if e != nil {
			result.SkippedFiles++
			return nil
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, searchMaxFileBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(raw) > searchMaxFileBytes {
			result.SkippedFiles++
			return nil
		}
		if result.BytesExamined+int64(len(raw)) > searchMaxBytes {
			result.Truncated = true
			return fs.SkipAll
		}
		result.BytesExamined += int64(len(raw))
		if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
			result.SkippedFiles++
			return nil
		}
		for n, line := range bytes.Split(raw, []byte{'\n'}) {
			if at := bytes.Index(line, needle); at >= 0 {
				result.Matches = append(result.Matches, SearchMatch{Path: filepath.ToSlash(rel), Line: n + 1, Text: boundedSearchLine(line, at)})
				if len(result.Matches) >= limit {
					result.Truncated = true
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	return result, err
}

func boundedSearchLine(line []byte, matchAt int) string {
	if len(line) <= searchMaxLineBytes {
		return string(line)
	}
	start := matchAt - searchMaxLineBytes/4
	if start < 0 {
		start = 0
	}
	if start+searchMaxLineBytes > len(line) {
		start = len(line) - searchMaxLineBytes
	}
	end := start + searchMaxLineBytes
	for start < end && !utf8.RuneStart(line[start]) {
		start++
	}
	for end > start && !utf8.Valid(line[start:end]) {
		end--
	}
	return string(line[start:end])
}

func (m *Manager) WriteFile(ctx context.Context, h Handle, relative string, contents []byte) (Handle, error) {
	if m.Grant.Owner != "" && !m.Grant.Allows("effect", "fs_write") {
		return Handle{}, fmt.Errorf("%w: fs_write", ErrDenied)
	}
	if len(contents) > writeMaxFileBytes {
		return Handle{}, fmt.Errorf("capsule fs: contents exceed %d byte write limit", writeMaxFileBytes)
	}
	path, err := m.resolve(ctx, h, relative, false)
	if err != nil {
		return Handle{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Handle{}, err
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return Handle{}, err
	}
	return m.mark(ctx, h, StateDirty, "capsule.workspace.changed")
}

// RunCommand runs only a definition-declared argv unless both the definition
// and immutable grant opt into raw argv. There is no shell mode.
func (m *Manager) RunCommand(ctx context.Context, h Handle, commandID string, rawArgv []string, timeout time.Duration) (CommandResult, error) {
	in, err := m.Status(ctx, h)
	if err != nil {
		return CommandResult{}, err
	}
	if !m.Grant.Allows("effect", "exec") {
		return CommandResult{}, fmt.Errorf("%w: exec", ErrDenied)
	}
	def, err := m.Definitions.Get(ctx, in.DefinitionID)
	if err != nil {
		return CommandResult{}, err
	}
	argv := append([]string(nil), rawArgv...)
	if commandID != "" {
		cmd, ok := def.Policy.Commands[commandID]
		if !ok {
			return CommandResult{}, fmt.Errorf("%w: command %q", ErrDenied, commandID)
		}
		argv = append([]string(nil), cmd.Argv...)
		if timeout == 0 && cmd.Timeout != "" {
			timeout, err = time.ParseDuration(cmd.Timeout)
			if err != nil {
				return CommandResult{}, fmt.Errorf("capsule exec: command timeout: %w", err)
			}
		}
	} else if !def.Policy.RawArgv || !m.Grant.Allows("effect", "raw_exec") {
		return CommandResult{}, fmt.Errorf("%w: raw argv", ErrDenied)
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return CommandResult{}, fmt.Errorf("capsule exec: argv is required")
	}
	path, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return CommandResult{}, err
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = path
	output := boundedCommandOutput{limit: commandMaxOutputBytes}
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if runCtx.Err() == context.DeadlineExceeded {
			exit = -1
		} else {
			return CommandResult{}, fmt.Errorf("capsule exec: %w", err)
		}
	}
	next, markErr := m.mark(ctx, h, StateDirty, "capsule.workspace.changed")
	if markErr != nil {
		return CommandResult{}, markErr
	}
	return CommandResult{Handle: next, ExitCode: exit, Output: output.String(), OutputTruncated: output.truncated, TimedOut: runCtx.Err() == context.DeadlineExceeded}, nil
}

func (m *Manager) StatusVCS(ctx context.Context, h Handle) (VCSStatus, error) {
	path, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return VCSStatus{}, err
	}
	out, err := git(ctx, path, "status", "--porcelain", "--branch")
	if err != nil {
		return VCSStatus{}, err
	}
	head, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return VCSStatus{}, err
	}
	branch, err := git(ctx, path, "branch", "--show-current")
	if err != nil {
		return VCSStatus{}, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	dirty := false
	for _, line := range lines {
		if line != "" && !strings.HasPrefix(line, "## ") {
			dirty = true
			break
		}
	}
	return VCSStatus{Handle: h, Branch: strings.TrimSpace(branch), Head: strings.TrimSpace(head), Dirty: dirty, Porcelain: out}, nil
}
func (m *Manager) DiffVCS(ctx context.Context, h Handle) (string, error) {
	path, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return "", err
	}
	return git(ctx, path, "diff", "--no-ext-diff", "HEAD")
}

// CommitResult reports what CommitVCS actually did. Committing new work and
// adopting work that git already committed are both success, and both say so.
type CommitResult struct {
	Handle    Handle    `json:"workspace"`
	Drift     HeadDrift `json:"drift"`
	Committed bool      `json:"committed"`
	Adopted   bool      `json:"adopted"`
	Previous  string    `json:"previous_head,omitempty"`
	Head      string    `json:"head,omitempty"`
	Commits   int       `json:"commits_adopted,omitempty"`
	Summary   string    `json:"summary"`
}

// CommitVCS is the handle-shaped wrapper kept for existing callers.
func (m *Manager) CommitVCS(ctx context.Context, h Handle, message string) (Handle, error) {
	result, err := m.CommitVCSResult(ctx, h, message)
	if err != nil {
		return Handle{}, err
	}
	return result.Handle, nil
}

// CommitVCSResult commits outstanding working-tree changes and reconciles the
// registered head with git.
//
// Raw `git commit` inside a workspace is a first-class, supported path. An
// agent that did the obvious thing gets its work adopted here, not an error:
// when there is nothing left to stage but git HEAD has advanced past the
// registered head with a clean tree, the head is adopted and reported. The
// only remaining error states are (a) genuinely nothing to do, which says so,
// and (b) a rewrite that would drop registered history, which fails loudly.
func (m *Manager) CommitVCSResult(ctx context.Context, h Handle, message string) (CommitResult, error) {
	if strings.TrimSpace(message) == "" {
		return CommitResult{}, fmt.Errorf("capsule vcs: message is required")
	}
	if !m.Grant.Allows("effect", "vcs_commit") {
		return CommitResult{}, fmt.Errorf("%w: vcs_commit", ErrDenied)
	}
	drift, err := m.InspectHead(ctx, h)
	if err != nil {
		return CommitResult{}, err
	}
	// Fail closed before touching the index: a rewrite must never be laundered
	// into the registered head by layering one more commit on top of it.
	if !drift.Relation.Adoptable() && drift.Relation != HeadInSync {
		return CommitResult{}, drift.refusal("refusing to commit")
	}
	path := drift.Path
	if _, err = git(ctx, path, "add", "-A"); err != nil {
		return CommitResult{}, err
	}
	if _, err = git(ctx, path, "diff", "--cached", "--quiet"); err != nil {
		if _, err = git(ctx, path, "commit", "--signoff", "-m", message); err != nil {
			return CommitResult{}, err
		}
		head, err := git(ctx, path, "rev-parse", "HEAD")
		if err != nil {
			return CommitResult{}, err
		}
		head = strings.TrimSpace(head)
		adopted := 0
		if drift.RegisteredHead != "" {
			if out, err := git(ctx, path, "rev-list", "--count", drift.RegisteredHead+".."+head); err == nil {
				fmt.Sscanf(strings.TrimSpace(out), "%d", &adopted)
			}
		}
		handle, err := m.markWith(ctx, drift.Handle, StateCommitted, "capsule.workspace.committed", func(in *Instance) {
			in.Head = head
		})
		if err != nil {
			return CommitResult{}, err
		}
		summary := fmt.Sprintf("committed working-tree changes; registered head advanced %s -> %s", shortSHA(drift.RegisteredHead), shortSHA(head))
		if adopted > 1 {
			summary = fmt.Sprintf("%s, %d commit(s) adopted (%d already existed from git)", summary, adopted, adopted-1)
		}
		return CommitResult{Handle: handle, Drift: drift, Committed: true, Adopted: adopted > 1, Previous: drift.RegisteredHead, Head: head, Commits: adopted, Summary: summary}, nil
	}
	// Nothing to stage. That is the normal outcome when the agent already
	// committed with git; adopt what git recorded instead of refusing.
	if drift.Relation.Adoptable() {
		adoption, err := m.adopt(ctx, drift)
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Handle: adoption.Handle, Drift: drift, Adopted: adoption.Adopted, Previous: adoption.Previous, Head: adoption.Head, Commits: adoption.Commits, Summary: adoption.Summary}, nil
	}
	return CommitResult{}, fmt.Errorf("%w: %s; %s", ErrNothingToCommit, drift.Explain(), drift.Next())
}

// MarkIntegrated advances lifecycle only after a reconciler completed its
// compare-and-swap update. It deliberately cannot perform Git operations.
func (m *Manager) MarkIntegrated(ctx context.Context, h Handle) (Handle, error) {
	return m.mark(ctx, h, StateIntegrated, "capsule.workspace.integrated")
}

// Integrate delegates a complete provider-owned integration lifecycle (such as
// the protected development compatibility adapter), then advances the handle
// only after that provider has succeeded. It cannot be used by an ungranted
// agent-facing server.
func (m *Manager) Integrate(ctx context.Context, h Handle, gate string) (Handle, error) {
	if !m.Grant.Allows("effect", "local_reconcile") {
		return Handle{}, fmt.Errorf("%w: local_reconcile", ErrDenied)
	}
	in, err := m.Status(ctx, h)
	if err != nil {
		return Handle{}, err
	}
	provider := m.Providers[in.Provider]
	integrator, ok := provider.(WorkspaceIntegrator)
	if !ok {
		return Handle{}, fmt.Errorf("capsule workspace: provider %q does not own integration", in.Provider)
	}
	definition, err := m.Definitions.Get(ctx, in.DefinitionID)
	if err != nil {
		return Handle{}, err
	}
	if err := integrator.Integrate(ctx, definition, in, gate); err != nil {
		return Handle{}, err
	}
	return m.MarkIntegrated(ctx, h)
}

func (m *Manager) resolve(ctx context.Context, h Handle, relative string, mustExist bool) (string, error) {
	root, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return "", err
	}
	path, err := ResolveWorkspacePath(root, relative, mustExist)
	if err != nil {
		return "", err
	}
	// Writes resolve with mustExist=false so a new path can be created. If the
	// target already exists, resolve it again to prevent os.WriteFile from
	// following a final symlink outside the workspace or into verifier assets.
	if !mustExist {
		if _, statErr := os.Lstat(path); statErr == nil {
			path, err = ResolveWorkspacePath(root, relative, true)
			if err != nil {
				return "", err
			}
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		} else {
			realParent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
			if parentErr != nil {
				return "", parentErr
			}
			path = filepath.Join(realParent, filepath.Base(path))
		}
	}
	if err := ensureAgentVisiblePath(root, path); err != nil {
		return "", err
	}
	return path, nil
}

func ensureAgentVisiblePath(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("capsule fs: path escapes workspace")
	}
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		if part == ".git" || part == ".kitsoki-verifier" {
			return fmt.Errorf("%w: agent-hidden workspace path", ErrDenied)
		}
	}
	return nil
}
func (m *Manager) mark(ctx context.Context, h Handle, state State, event string) (Handle, error) {
	return m.markWith(ctx, h, state, event, nil)
}

func (m *Manager) markWith(ctx context.Context, h Handle, state State, event string, update func(*Instance)) (Handle, error) {
	in, err := m.Instances.CompareAndSwap(ctx, h.ID, h.Generation, func(cur *Instance) error {
		if cur.State == StateClosed {
			return ErrInvalidState
		}
		cur.State = state
		if update != nil {
			update(cur)
		}
		return nil
	})
	if err != nil {
		return Handle{}, err
	}
	if err := m.emit(ctx, event, in); err != nil {
		return Handle{}, err
	}
	return Handle{ID: in.ID, Generation: in.Generation}, nil
}
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("capsule vcs: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
