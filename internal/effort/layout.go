// Package effort is the on-disk shell around the work graph: where an
// effort's artifacts live, how they are read and written, and the append-only
// log that makes a killed run resumable.
//
// One directory per effort, under .anoikis/ at the repository root. Every
// artifact in it is a frozen-schema file validated on the way in and on the
// way out, every write is atomic, and every write is serial — the single
// writer is what makes the store race-free without a lock protocol.
package effort

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// DirName is the effort-directory convention: one directory per effort,
// tracked in git so a build can resume and hand off across sessions.
const DirName = ".anoikis"

// The subdirectories whose contents die with the run and are never committed:
// a raw per-run log, whose durable slice is copied into the run's result, and
// a node's own git worktree, which is recreated from its base commit on
// demand.
const (
	logsDir      = "logs"
	worktreesDir = "worktrees"
)

// Ephemeral names every uncommitted subdirectory of an effort. Each
// enumerator that must agree about this set — the ignore file, the directory
// tree an effort is created with, artifact discovery — reads it from here, so
// they cannot drift apart.
var Ephemeral = []string{logsDir, worktreesDir}

// FilePerm is the mode every artifact this package writes is created with.
const FilePerm fs.FileMode = 0o644

// dirPerm is the mode every directory this package creates is made with.
const dirPerm fs.FileMode = 0o755

// Layout resolves every artifact path for one effort. It performs no I/O
// beyond what the constructors need to find the directory, so a path can be
// computed and reported without touching the artifact behind it.
type Layout struct {
	// Root is the repository root the effort directory hangs off.
	Root string
	// Slug names the effort within .anoikis/.
	Slug string
}

// Dir is the effort's own directory.
func (l Layout) Dir() string { return filepath.Join(l.Root, DirName, l.Slug) }

// Project is the effort manifest.
func (l Layout) Project() string { return filepath.Join(l.Dir(), "project.json") }

// ProjectMirror is the manifest's generated Markdown view.
func (l Layout) ProjectMirror() string { return filepath.Join(l.Dir(), "project.md") }

// Index is the tiny top of the sharded graph.
func (l Layout) Index() string { return filepath.Join(l.Dir(), "graph.json") }

// ShardDir holds one file per gate shard.
func (l Layout) ShardDir() string { return filepath.Join(l.Dir(), "graph") }

// Shard is one gate's slice of the graph.
func (l Layout) Shard(gateID string) string {
	return filepath.Join(l.ShardDir(), FileKey(gateID)+".json")
}

// NodeDir holds one detail record per node.
func (l Layout) NodeDir() string { return filepath.Join(l.Dir(), "nodes") }

// Detail is one node's detail record.
func (l Layout) Detail(nodeID string) string {
	return filepath.Join(l.NodeDir(), FileKey(nodeID)+".json")
}

// Gates is the gate-policy artifact.
func (l Layout) Gates() string { return filepath.Join(l.Dir(), "gates.json") }

// RunLog is the append-only transition log.
func (l Layout) RunLog() string { return filepath.Join(l.Dir(), "run-log.jsonl") }

// Cursor is the resume cursor: how far into the run log has already been
// sealed, so a resume reads the tail rather than the history.
func (l Layout) Cursor() string { return filepath.Join(l.Dir(), "resume-cursor.json") }

// Findings is the ranked findings register.
func (l Layout) Findings() string { return filepath.Join(l.Dir(), "findings.json") }

// FindingsMirror is the register's generated Markdown view.
func (l Layout) FindingsMirror() string { return filepath.Join(l.Dir(), "findings.md") }

// ResultDir holds one durable run result per node.
func (l Layout) ResultDir() string { return filepath.Join(l.Dir(), "results") }

// Result is one node's durable run result.
func (l Layout) Result(nodeID string) string {
	return filepath.Join(l.ResultDir(), FileKey(nodeID)+".json")
}

// PromptDir holds every rendered dispatch prompt.
func (l Layout) PromptDir() string { return filepath.Join(l.Dir(), "prompts") }

// Prompt is one run's verbatim rendered prompt, the artifact a resume
// replays from.
func (l Layout) Prompt(runID string) string {
	return filepath.Join(l.PromptDir(), FileKey(runID)+".txt")
}

// ArchiveDir holds closed nodes moved out of the hot path.
func (l Layout) ArchiveDir() string { return filepath.Join(l.Dir(), "archive", "nodes") }

// ArchivedDetail is where a closed node's detail record is moved to.
func (l Layout) ArchivedDetail(nodeID string) string {
	return filepath.Join(l.ArchiveDir(), FileKey(nodeID)+".json")
}

// LogDir holds raw per-run logs. Its contents are ephemeral and never
// committed.
func (l Layout) LogDir() string { return filepath.Join(l.Dir(), logsDir) }

// Worktree is where a node's own git worktree is checked out. One per node,
// keyed by its id, so every node's output is path-disjoint by construction.
func (l Layout) Worktree(nodeID string) string {
	return filepath.Join(l.Dir(), worktreesDir, FileKey(nodeID))
}

// Rel renders an absolute artifact path relative to the effort directory,
// which is the form every stored reference takes so an effort can be moved
// without rewriting its own contents.
func (l Layout) Rel(path string) string {
	rel, err := filepath.Rel(l.Dir(), path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// Resolve turns a stored effort-relative reference back into a real path.
func (l Layout) Resolve(ref string) string {
	if filepath.IsAbs(ref) {
		return ref
	}
	return filepath.Join(l.Dir(), filepath.FromSlash(ref))
}

// ensureDirs creates every directory an effort's artifacts live in, committed
// and ephemeral alike.
func (l Layout) ensureDirs() error {
	dirs := []string{l.Dir(), l.ShardDir(), l.NodeDir(), l.ResultDir(), l.PromptDir(), l.ArchiveDir()}
	for _, name := range Ephemeral {
		dirs = append(dirs, filepath.Join(l.Dir(), name))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, dirPerm); err != nil {
			return fmt.Errorf("effort: create %s: %w", d, err)
		}
	}
	return nil
}

// Open resolves the effort named by slug under root, requiring it to exist.
func Open(root, slug string) (Layout, error) {
	l := Layout{Root: root, Slug: slug}
	if slug == "" {
		return Layout{}, fmt.Errorf("effort: a slug is required; %s", listHint(root))
	}
	if fi, err := os.Stat(l.Dir()); err != nil || !fi.IsDir() {
		return Layout{}, fmt.Errorf("effort: no effort %q under %s; %s", slug, filepath.Join(root, DirName), listHint(root))
	}
	return l, nil
}

// Create resolves the effort named by slug under root, creating its
// directory tree when absent.
func Create(root, slug string) (Layout, error) {
	if slug == "" {
		return Layout{}, fmt.Errorf("effort: a slug is required")
	}
	l := Layout{Root: root, Slug: slug}
	if err := l.ensureDirs(); err != nil {
		return Layout{}, err
	}
	return l, nil
}

// List returns every effort slug under root, sorted.
func List(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, DirName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("effort: list %s: %w", filepath.Join(root, DirName), err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
}

// listHint renders the available slugs for an error message.
func listHint(root string) string {
	slugs, err := List(root)
	if err != nil || len(slugs) == 0 {
		return "no efforts exist yet"
	}
	return "available: " + strings.Join(slugs, ", ")
}

// FileKey renders an id as a filename component that is safe on every
// filesystem and still recognisable. An id already safe is used verbatim;
// anything else is transliterated and given a digest suffix, so two ids that
// transliterate alike never collide on one file.
func FileKey(id string) string {
	if id != "" && safeKey(id) {
		return id
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	sum := sha256.Sum256([]byte(id))
	return strings.Trim(b.String(), ".") + "-" + hex.EncodeToString(sum[:4])
}

// safeKey reports whether id can serve as a filename component unchanged.
func safeKey(id string) bool {
	if strings.HasPrefix(id, ".") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
