package discovery_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/discovery"
)

func TestGather_MalformedYAMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.md")
	body := "---\ntags: [unterminated\n---\n\n# Bad\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := discovery.Gather(dir)
	if err == nil {
		t.Fatal("Gather over malformed YAML frontmatter: want error, got nil")
	}
}

func TestGather_NonexistentDirReturnsError(t *testing.T) {
	_, err := discovery.Gather(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("Gather over nonexistent dir: want error, got nil")
	}
}

func TestLocateDesign_EmptyProjectHomeIsUndetermined(t *testing.T) {
	dir := t.TempDir()
	_, _, err := discovery.LocateDesign(dir)
	if !errors.Is(err, discovery.ErrTypeUndetermined) {
		t.Fatalf("error = %v, want ErrTypeUndetermined", err)
	}
}

func TestSelect_DuplicateTypeTagWithinOneDocumentUsesFirst(t *testing.T) {
	// A single document declaring "type" twice must not be treated as two
	// candidates (that would be a Gather bug, not a Select ambiguity).
	dir := t.TempDir()
	path := filepath.Join(dir, "weird.md")
	body := "---\ntags:\n  - type:design\n  - type:plan\n  - status:complete\n---\n\n# X\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, route, err := discovery.LocateDesign(dir)
	if err != nil {
		t.Fatalf("LocateDesign: %v", err)
	}
	if route != discovery.Derive {
		t.Fatalf("route = %q, want %q (first-declared type tag should win)", route, discovery.Derive)
	}
	_ = got
}

func TestSelect_CaseSensitiveTypeValueDoesNotMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.md")
	body := "---\ntags:\n  - type:Design\n  - status:complete\n---\n\n# X\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := discovery.LocateDesign(dir)
	if !errors.Is(err, discovery.ErrTypeUndetermined) {
		t.Fatalf("error = %v, want ErrTypeUndetermined for a differently-cased type value (documenting exact-match semantics)", err)
	}
}

func TestSelect_AmbiguityErrorListsCandidatesInDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "z.md"), []byte("---\ntags:\n  - type:design\n  - status:complete\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("---\ntags:\n  - type:design\n  - status:stub\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := discovery.LocateDesign(dir)
	if err == nil {
		t.Fatal("want ambiguity error")
	}
	msg := err.Error()
	if strings.Index(msg, "a.md") > strings.Index(msg, "z.md") {
		t.Fatalf("error %q does not list candidates in sorted path order", msg)
	}
}
