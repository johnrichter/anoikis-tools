package schemaforward_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dispatch/schemaforward"
)

// canonicalSchema is a small representative contract: an object with two required string
// fields. Its exact formatting (two-space indent, this key order) is what "byte-for-byte after
// canonicalization" is checked against.
const canonicalSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["node_id", "run_id"],
  "properties": {
    "node_id": {"type": "string"},
    "run_id": {"type": "string"}
  }
}
`

// reformattedEquivalent is canonicalSchema with different whitespace and top-level/nested key
// order — no key, value or array element changed. RFC 8785 canonicalization treats this as
// identical to canonicalSchema.
const reformattedEquivalent = `{
"properties": {"run_id": {"type": "string"}, "node_id": {"type": "string"}},
"required": ["node_id", "run_id"],
"additionalProperties": false,
"type": "object",
"$schema": "https://json-schema.org/draft/2020-12/schema"
}`

// oneKeyMistyped is canonicalSchema with a single properties key retyped from memory —
// "node_id" became "nod_id" — the FB7 regression: exactly one key, nothing else changed.
const oneKeyMistyped = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["node_id", "run_id"],
  "properties": {
    "nod_id": {"type": "string"},
    "run_id": {"type": "string"}
  }
}
`

// requiredReordered is canonicalSchema with the "required" array's two elements swapped. Both
// documents describe the same set of required fields — semantically equivalent as a schema —
// but they are not the same JSON: canonicalization sorts object keys, never array elements.
const requiredReordered = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["run_id", "node_id"],
  "properties": {
    "node_id": {"type": "string"},
    "run_id": {"type": "string"}
  }
}
`

// writeSchema writes content to name inside t.TempDir() and returns the path.
func writeSchema(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return path
}

func TestForward_ReturnsCanonicalBytesUnmodified(t *testing.T) {
	path := writeSchema(t, "canonical.schema.json", canonicalSchema)
	got, err := schemaforward.Forward(schemaforward.Config{SchemaPath: path})
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if !bytes.Equal(got, []byte(canonicalSchema)) {
		t.Fatalf("Forward returned altered bytes:\ngot:  %q\nwant: %q", got, canonicalSchema)
	}
}

func TestForward_MissingCanonicalFileIsNamedErrorNeverEmptyPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.schema.json")
	got, err := schemaforward.Forward(schemaforward.Config{SchemaPath: path})
	if err == nil {
		t.Fatal("Forward against a missing canonical file: want error, got nil")
	}
	if !errors.Is(err, schemaforward.ErrCanonicalUnreadable) {
		t.Fatalf("Forward error = %v, want it to match ErrCanonicalUnreadable", err)
	}
	if got != nil {
		t.Fatalf("Forward returned a non-nil payload alongside an error: %q", got)
	}
}

func TestForward_UnconfiguredPathIsNamedError(t *testing.T) {
	_, err := schemaforward.Forward(schemaforward.Config{})
	if !errors.Is(err, schemaforward.ErrNoSchemaConfigured) {
		t.Fatalf("Forward with an empty SchemaPath: error = %v, want ErrNoSchemaConfigured", err)
	}
}

func TestVerify_IdenticalCopyReturnsCanonicalBytes(t *testing.T) {
	path := writeSchema(t, "canonical.schema.json", canonicalSchema)
	cfg := schemaforward.Config{SchemaPath: path}
	got, err := schemaforward.Verify(cfg, []byte(canonicalSchema))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !bytes.Equal(got, []byte(canonicalSchema)) {
		t.Fatalf("Verify returned %q, want the canonical bytes %q", got, canonicalSchema)
	}
}

func TestVerify_ReformattedButEquivalentCopyIsAccepted(t *testing.T) {
	path := writeSchema(t, "canonical.schema.json", canonicalSchema)
	cfg := schemaforward.Config{SchemaPath: path}
	got, err := schemaforward.Verify(cfg, []byte(reformattedEquivalent))
	if err != nil {
		t.Fatalf("Verify rejected a whitespace/key-order-only variant: %v", err)
	}
	// Verify returns the canonical file's own bytes on a match, never the caller's copy — a
	// downstream payload always carries the one canonical serialization.
	if !bytes.Equal(got, []byte(canonicalSchema)) {
		t.Fatalf("Verify returned %q, want the canonical bytes %q", got, canonicalSchema)
	}
}

func TestVerify_OneMistypedKeyIsRefused_FB7Regression(t *testing.T) {
	path := writeSchema(t, "canonical.schema.json", canonicalSchema)
	cfg := schemaforward.Config{SchemaPath: path}
	got, err := schemaforward.Verify(cfg, []byte(oneKeyMistyped))
	if err == nil {
		t.Fatal("Verify accepted a copy with one mistyped key")
	}
	if !errors.Is(err, schemaforward.ErrMismatch) {
		t.Fatalf("Verify error = %v, want it to match ErrMismatch", err)
	}
	if got != nil {
		t.Fatalf("Verify returned a non-nil payload alongside a mismatch error: %q", got)
	}
}

func TestVerify_SemanticallyEquivalentButNonIdenticalCopyIsRefused(t *testing.T) {
	path := writeSchema(t, "canonical.schema.json", canonicalSchema)
	cfg := schemaforward.Config{SchemaPath: path}
	got, err := schemaforward.Verify(cfg, []byte(requiredReordered))
	if err == nil {
		t.Fatal("Verify accepted a copy whose required array was reordered")
	}
	if !errors.Is(err, schemaforward.ErrMismatch) {
		t.Fatalf("Verify error = %v, want it to match ErrMismatch", err)
	}
	if got != nil {
		t.Fatalf("Verify returned a non-nil payload alongside a mismatch error: %q", got)
	}
}

func TestVerify_InvalidJSONCallerCopyIsRefused(t *testing.T) {
	path := writeSchema(t, "canonical.schema.json", canonicalSchema)
	cfg := schemaforward.Config{SchemaPath: path}
	_, err := schemaforward.Verify(cfg, []byte("{not json"))
	if !errors.Is(err, schemaforward.ErrInvalidJSON) {
		t.Fatalf("Verify error = %v, want it to match ErrInvalidJSON", err)
	}
}

func TestVerify_MissingCanonicalFileNeverFallsBackToCallerCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.schema.json")
	cfg := schemaforward.Config{SchemaPath: path}
	got, err := schemaforward.Verify(cfg, []byte(canonicalSchema))
	if !errors.Is(err, schemaforward.ErrCanonicalUnreadable) {
		t.Fatalf("Verify error = %v, want it to match ErrCanonicalUnreadable", err)
	}
	if got != nil {
		t.Fatalf("Verify fell back to the caller-supplied copy on a missing canonical file: %q", got)
	}
}
