package discovery_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/discovery"
)

// writeDoc writes a Markdown document with the given frontmatter tags (or no
// frontmatter at all when tags is nil) into dir, under name.
func writeDoc(t *testing.T, dir, name string, tags []string) string {
	t.Helper()
	var body strings.Builder
	if tags != nil {
		body.WriteString("---\nname: fixture\n")
		body.WriteString("tags:\n")
		for _, tag := range tags {
			body.WriteString("  - " + tag + "\n")
		}
		body.WriteString("---\n\n")
	}
	body.WriteString("# Fixture\n")
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return path
}

func TestLocateDesign_CompleteUnderNonCanonicalFilenameRoutesToDerive(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "initial-design.md", []string{"type:design", "status:complete"})

	got, route, err := discovery.LocateDesign(dir)
	if err != nil {
		t.Fatalf("LocateDesign: %v", err)
	}
	if route != discovery.Derive {
		t.Fatalf("route = %q, want %q", route, discovery.Derive)
	}
	if got.Path != filepath.Join(dir, "initial-design.md") {
		t.Fatalf("resolved path = %q, want the non-canonical filename", got.Path)
	}
}

func TestLocateDesign_StubUnderCanonicalFilenameRoutesToResumeDraft(t *testing.T) {
	dir := t.TempDir()
	// The canonical filename is used here deliberately: routing must come
	// from the declared type/status, not from recognizing this name.
	writeDoc(t, dir, "design.md", []string{"type:design", "status:stub"})

	_, route, err := discovery.LocateDesign(dir)
	if err != nil {
		t.Fatalf("LocateDesign: %v", err)
	}
	if route != discovery.ResumeDraft {
		t.Fatalf("route = %q, want %q", route, discovery.ResumeDraft)
	}
}

func TestSelect_MultipleCandidatesSameTypeIsNamedAmbiguityError(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.md", []string{"type:design", "status:complete"})
	writeDoc(t, dir, "b.md", []string{"type:design", "status:stub"})

	_, _, err := discovery.LocateDesign(dir)
	if err == nil {
		t.Fatal("LocateDesign with two same-typed candidates: want error, got nil")
	}
	if !errors.Is(err, discovery.ErrAmbiguousType) {
		t.Fatalf("error = %v, want it to match ErrAmbiguousType", err)
	}
	if !strings.Contains(err.Error(), "a.md") || !strings.Contains(err.Error(), "b.md") {
		t.Fatalf("error %q does not name both candidates", err.Error())
	}
}

func TestSelect_NoDeclaredTypeIsUndeterminedNamingTheField(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "notes.md", nil) // no frontmatter at all

	_, _, err := discovery.LocateDesign(dir)
	if err == nil {
		t.Fatal("LocateDesign over a document with no declared type: want error, got nil")
	}
	if !errors.Is(err, discovery.ErrTypeUndetermined) {
		t.Fatalf("error = %v, want it to match ErrTypeUndetermined", err)
	}
	if !strings.Contains(err.Error(), "type") {
		t.Fatalf("error %q does not name the missing field", err.Error())
	}
}

func TestSelect_UnrecognizedTypeValueIsUndetermined(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "notes.md", []string{"type:scratchpad", "status:complete"})

	_, _, err := discovery.LocateDesign(dir)
	if !errors.Is(err, discovery.ErrTypeUndetermined) {
		t.Fatalf("error = %v, want it to match ErrTypeUndetermined", err)
	}
}

func TestClassifyStatus_UnrecognizedStatusIsNamedError(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "design.md", []string{"type:design", "status:archived"})

	_, _, err := discovery.LocateDesign(dir)
	if !errors.Is(err, discovery.ErrStatusUndetermined) {
		t.Fatalf("error = %v, want it to match ErrStatusUndetermined", err)
	}
}

func TestGather_SkipsSubdirectoriesAndNonMarkdownFiles(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "design.md", []string{"type:design", "status:complete"})
	if err := os.WriteFile(filepath.Join(dir, "notes.json"), []byte(`{"type":"design"}`), 0o644); err != nil {
		t.Fatalf("write notes.json fixture: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "type-design-lookalike.md"), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}

	candidates, err := discovery.Gather(dir)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("Gather returned %d candidates, want 1 (the JSON file and the directory must be skipped)", len(candidates))
	}
}

// TestNoDocumentFilenameLiteral is a sanity check for acceptance criterion 1:
// nothing in the discovery path selects by a hardcoded document filename.
// The full mechanical assertion is the test suite's; this guards the
// property while the package is being built.
func TestNoDocumentFilenameLiteral(t *testing.T) {
	forbidden := []string{"design.md", "plan.md", "execution.md", "project.md", "feedback.md"}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read discovery package dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		body := string(raw)
		for _, literal := range forbidden {
			if strings.Contains(body, literal) {
				t.Errorf("%s contains the document-filename literal %q", e.Name(), literal)
			}
		}
	}
}
