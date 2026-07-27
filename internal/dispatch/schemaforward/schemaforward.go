// Package schemaforward is the one route a JSON Schema takes into a dispatch payload.
//
// FB7's root cause was an orchestrator retyping a schema from memory into a tool-call payload
// and mistyping one key — strict-mode validation then rejected every downstream emit, and the
// mismatch stayed buried in each agent's own transcript for a full extra fix round before
// anyone traced it back to a malformed contract rather than an agent defect. The fix is
// mechanical, not a stronger instruction: a schema either comes from Forward, read straight off
// the canonical file and returned unmodified, or from Verify, which checks a caller-supplied
// copy against that same file and refuses on any mismatch. Neither path ever lets a model's own
// transcription reach a dispatch payload uncontested.
package schemaforward

import (
	"bytes"
	"fmt"
	"os"

	"github.com/johnrichter/claude-shared-tooling/go/jsondoc"
)

// Named refusals. Each is returned via %w so a caller can match it with errors.Is while still
// getting the offending path in the message; none of them is ever absorbed into a fallback
// value — every one pairs with a nil result.
var (
	// ErrNoSchemaConfigured reports a Config whose SchemaPath was never populated. Config is
	// the declared config key this package resolves the canonical path from; an empty path
	// means that key was never wired up, not that no schema is required.
	ErrNoSchemaConfigured = fmt.Errorf("schemaforward: no canonical schema path configured")

	// ErrCanonicalUnreadable reports a canonical schema file that does not exist or cannot be
	// read. It is refused outright — never treated as "forward nothing" or "fall back to the
	// caller's copy".
	ErrCanonicalUnreadable = fmt.Errorf("schemaforward: canonical schema file is missing or unreadable")

	// ErrInvalidJSON reports a caller-supplied copy that is not valid JSON at all, so it cannot
	// even be canonicalized for comparison.
	ErrInvalidJSON = fmt.Errorf("schemaforward: caller-supplied schema is not valid JSON")

	// ErrMismatch reports a caller-supplied copy that, once canonicalized, is not byte-for-byte
	// identical to the canonical file. This is the FB7 regression: a single mistyped key never
	// canonicalizes to the same bytes as the key it replaced.
	ErrMismatch = fmt.Errorf("schemaforward: caller-supplied schema does not match its canonical file")
)

// Config names, through one declared config key, the canonical schema file a dispatch payload
// may carry. It is deliberately just a path: the value a caller's own layered settings resolve
// (flag > env > file > default, same layering the rest of this CLI already uses) — never a
// schema body, and never a literal a prompt template writes out itself.
type Config struct {
	// SchemaPath is the canonical *.schema.json file this verb forwards, or validates a
	// caller-supplied copy against.
	SchemaPath string `koanf:"schema_path"`
}

// Forward reads cfg's canonical schema file and returns its bytes exactly as stored — the route
// a dispatch payload takes to a schema when it carries no caller-supplied copy to check.
func Forward(cfg Config) ([]byte, error) {
	if cfg.SchemaPath == "" {
		return nil, ErrNoSchemaConfigured
	}
	b, err := os.ReadFile(cfg.SchemaPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCanonicalUnreadable, cfg.SchemaPath, err)
	}
	return b, nil
}

// Verify checks callerCopy against cfg's canonical file and, only on an exact match, returns
// the canonical bytes — never callerCopy itself, so a downstream payload never carries two
// differently-sourced-but-declared-equal copies of the same contract.
//
// A match is byte-for-byte identity after RFC 8785 canonicalization (jsondoc.CanonicalizeRaw):
// key order, whitespace and number formatting never register as a difference, but a renamed,
// added, removed or reordered-within-an-array key does. That is the declared rule — semantic
// schema equivalence is not evaluated, so a copy that validates the same documents but is not
// the same JSON (e.g. a reordered "required" array) is refused exactly as a mistyped key is.
func Verify(cfg Config, callerCopy []byte) ([]byte, error) {
	canonical, err := Forward(cfg)
	if err != nil {
		return nil, err
	}
	canonicalNorm, err := jsondoc.CanonicalizeRaw(canonical)
	if err != nil {
		return nil, fmt.Errorf("schemaforward: canonical file %s is not valid JSON: %w", cfg.SchemaPath, err)
	}
	callerNorm, err := jsondoc.CanonicalizeRaw(callerCopy)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidJSON, cfg.SchemaPath, err)
	}
	if !bytes.Equal(canonicalNorm, callerNorm) {
		return nil, fmt.Errorf("%w: %s", ErrMismatch, cfg.SchemaPath)
	}
	return canonical, nil
}
