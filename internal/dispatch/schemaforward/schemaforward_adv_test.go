package schemaforward_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dispatch/schemaforward"
)

// Adversarial probes beyond the implementer's own suite.

func TestForward_UnreadablePermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "no-read.schema.json")
	if err := os.WriteFile(path, []byte(canonicalSchema), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := schemaforward.Forward(schemaforward.Config{SchemaPath: path})
	if err == nil {
		t.Fatal("Forward against a permission-denied file: want error, got nil")
	}
	if !errors.Is(err, schemaforward.ErrCanonicalUnreadable) {
		t.Fatalf("Forward error = %v, want ErrCanonicalUnreadable", err)
	}
	if got != nil {
		t.Fatalf("Forward returned non-nil payload alongside error: %q", got)
	}
}

func TestForward_PathIsDirectoryNotFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "a-directory.schema.json")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := schemaforward.Forward(schemaforward.Config{SchemaPath: sub})
	if err == nil {
		t.Fatal("Forward against a directory path: want error, got nil")
	}
	if !errors.Is(err, schemaforward.ErrCanonicalUnreadable) {
		t.Fatalf("Forward error = %v, want ErrCanonicalUnreadable", err)
	}
	if got != nil {
		t.Fatalf("Forward returned non-nil payload for a directory path: %q", got)
	}
}

func TestVerify_EmptyCallerCopyIsRefused(t *testing.T) {
	path := writeSchema(t, "canonical.schema.json", canonicalSchema)
	cfg := schemaforward.Config{SchemaPath: path}
	got, err := schemaforward.Verify(cfg, []byte{})
	if err == nil {
		t.Fatal("Verify accepted an empty caller copy")
	}
	if !errors.Is(err, schemaforward.ErrInvalidJSON) {
		t.Fatalf("Verify error = %v, want ErrInvalidJSON", err)
	}
	if got != nil {
		t.Fatalf("Verify returned non-nil payload for an empty caller copy: %q", got)
	}
}

func TestVerify_NilCallerCopyIsRefused(t *testing.T) {
	path := writeSchema(t, "canonical.schema.json", canonicalSchema)
	cfg := schemaforward.Config{SchemaPath: path}
	got, err := schemaforward.Verify(cfg, nil)
	if err == nil {
		t.Fatal("Verify accepted a nil caller copy")
	}
	if got != nil {
		t.Fatalf("Verify returned non-nil payload for a nil caller copy: %q", got)
	}
}

func TestVerify_CanonicalFileCorruptIsRefusedNotForwarded(t *testing.T) {
	path := writeSchema(t, "canonical.schema.json", "{not json at all")
	cfg := schemaforward.Config{SchemaPath: path}
	got, err := schemaforward.Verify(cfg, []byte(canonicalSchema))
	if err == nil {
		t.Fatal("Verify against a corrupt canonical file: want error, got nil")
	}
	if got != nil {
		t.Fatalf("Verify returned non-nil payload with a corrupt canonical file: %q", got)
	}
}

func TestAssertOnlyRoute_DetectsLiteralInYAMLAndTmpl(t *testing.T) {
	root := t.TempDir()
	yamlBody := "prompt: |\n  schema: '{\"$schema\": \"https://json-schema.org/draft/2020-12/schema\"}'\n"
	if err := os.WriteFile(filepath.Join(root, "prompt.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write yaml fixture: %v", err)
	}
	tmplBody := "{\"properties\": {}, \"required\": []}\n"
	if err := os.WriteFile(filepath.Join(root, "orchestrator.tmpl"), []byte(tmplBody), 0o644); err != nil {
		t.Fatalf("write tmpl fixture: %v", err)
	}
	findings, err := schemaforward.AssertOnlyRoute(root)
	if err != nil {
		t.Fatalf("AssertOnlyRoute: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want 2 (yaml + tmpl planted literals)", findings)
	}
}

func TestAssertOnlyRoute_MissingRootIsNamedErrorNotEmptyPass(t *testing.T) {
	_, err := schemaforward.AssertOnlyRoute(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("AssertOnlyRoute against a missing root: want error, got nil")
	}
}
