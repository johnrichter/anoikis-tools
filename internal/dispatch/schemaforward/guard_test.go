package schemaforward_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dispatch/schemaforward"
)

// repoRoot returns the anoikis-tools checkout this test file lives in, derived from the test's
// own source path rather than the working directory `go test` happens to run from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve this test file's own path")
	}
	// .../internal/dispatch/schemaforward/guard_test.go -> repo root is three levels up.
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}

// TestAssertOnlyRoute_RepoIsClean checks the property this guard exists to protect: today, the
// only files carrying a schema-shaped literal are the canonical schema files themselves and
// this package's own fixtures — nowhere in dispatch code or a prompt template.
func TestAssertOnlyRoute_RepoIsClean(t *testing.T) {
	root := repoRoot(t)
	findings, err := schemaforward.AssertOnlyRoute(root, "schemas/anoikis", "internal/dispatch/schemaforward")
	if err != nil {
		t.Fatalf("AssertOnlyRoute: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("schema-shaped literal(s) found outside the canonical route: %+v", findings)
	}
}

// TestAssertOnlyRoute_DetectsPlantedLiteral proves the guard has teeth: a schema-shaped literal
// planted outside the exempt set is reported by path, not silently missed.
func TestAssertOnlyRoute_DetectsPlantedLiteral(t *testing.T) {
	root := t.TempDir()
	planted := filepath.Join(root, "dispatch_helper.go")
	body := "package dispatch\n\nconst hardcoded = `{\"$schema\": \"https://json-schema.org/draft/2020-12/schema\", \"type\": \"object\"}`\n"
	if err := os.WriteFile(planted, []byte(body), 0o644); err != nil {
		t.Fatalf("write planted fixture: %v", err)
	}
	findings, err := schemaforward.AssertOnlyRoute(root)
	if err != nil {
		t.Fatalf("AssertOnlyRoute: %v", err)
	}
	if len(findings) != 1 || findings[0].Path != planted {
		t.Fatalf("findings = %+v, want exactly one finding at %s", findings, planted)
	}
}

// TestAssertOnlyRoute_ExemptDirIsSkipped checks that a literal inside a declared exempt
// directory is not reported — the canonical schema files and this package's own fixtures must
// stay legal, or every future canonical schema addition would trip the guard.
func TestAssertOnlyRoute_ExemptDirIsSkipped(t *testing.T) {
	root := t.TempDir()
	exemptDir := filepath.Join(root, "canonical")
	if err := os.MkdirAll(exemptDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(exemptDir, "node.schema.json"), []byte(canonicalSchema), 0o644); err != nil {
		t.Fatalf("write exempt fixture: %v", err)
	}
	findings, err := schemaforward.AssertOnlyRoute(root, "canonical")
	if err != nil {
		t.Fatalf("AssertOnlyRoute: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none — the canonical file lives in a declared exempt dir", findings)
	}
}

// TestAssertOnlyRoute_NonSchemaFilesAreIgnored checks that ordinary source carrying neither
// signature never registers as a finding, so the guard's signal stays sparse.
func TestAssertOnlyRoute_NonSchemaFilesAreIgnored(t *testing.T) {
	root := t.TempDir()
	body := "package dispatch\n\n// properties of this type are unrelated to any schema.\nfunc ordinary() {}\n"
	if err := os.WriteFile(filepath.Join(root, "ordinary.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	findings, err := schemaforward.AssertOnlyRoute(root)
	if err != nil {
		t.Fatalf("AssertOnlyRoute: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none — no quoted schema signature is present", findings)
	}
}
