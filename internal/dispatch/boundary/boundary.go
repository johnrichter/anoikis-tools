// Package boundary is the one place a dispatched run's return is let through.
//
// A run's deliverable is written to a file inside its own worktree; the return message this
// package accepts is only ever a bounded manifest — status, artifact paths, a few key facts, the
// next action. A control-plane verdict (pass/fail, accept/fix) is itself one of these manifests,
// carried in its status field, not a separate shape. Nothing this package produces ever contains
// deliverable content: the manifest's artifact paths are the only route back to it, so a caller
// reading a Manifest is never tempted to re-read a spilled return to recover bytes it already
// paid for once.
//
// FB10 recorded three research-agent returns — 64KB, 54KB, 93KB — that carried their full
// findings as the message and had to be spilled to disk by the harness after the fact. That
// spill is not a path this package has: a return over its class's declared ceiling is refused
// before it is even parsed, never salvaged. FB7 recorded the other failure mode: a batch where
// every emit was schema-invalid, and every rejection was silently absorbed by a degraded-result
// fallback, so one cheap defect became four expensive review rounds. Validate and ValidateBatch
// never absorb: a rejection is always a named error, returned instead of a value, for the caller
// to surface — never traded for a placeholder result or a silent retry.
package boundary

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ReturnClass names one of the two shapes a dispatch return may declare, each held to its own
// declared byte ceiling. A control-plane verdict and a deliverable manifest share the same
// Manifest shape — a verdict is a manifest whose Status field carries the decision — so the
// class governs only which ceiling applies, never a different set of fields.
type ReturnClass string

const (
	// ClassControlPlane is a review or gate's pass/fail or accept/fix verdict: the smallest
	// legitimate return, so it is held to the tightest ceiling.
	ClassControlPlane ReturnClass = "control_plane_verdict"
	// ClassDeliverable is a builder or writer's report that its artifact is written to disk:
	// it may carry a short list of artifact paths and facts, so its ceiling is wider, but it
	// remains orders of magnitude below a full deliverable's size.
	ClassDeliverable ReturnClass = "deliverable_manifest"
)

// Declared byte ceilings, one per return class, in bytes of raw JSON. This is the only place
// either number is stated; every caller and every error message names the class and looks the
// number up here rather than restating it.
//
// Both ceilings sit two to three orders of magnitude below the FB10 overflow sizes (64KB, 54KB,
// 93KB), so a return anywhere near a real deliverable's size is refused, while a legitimately
// small manifest always fits.
const (
	controlPlaneCeilingBytes = 1024
	deliverableCeilingBytes  = 4096
)

// ceilings maps each declared return class to its byte ceiling.
var ceilings = map[ReturnClass]int{
	ClassControlPlane: controlPlaneCeilingBytes,
	ClassDeliverable:  deliverableCeilingBytes,
}

// maxFacts bounds "a few key facts" so the field can never grow into a second deliverable
// channel alongside the byte ceiling.
const maxFacts = 5

// Ceiling returns c's declared byte ceiling.
func (c ReturnClass) Ceiling() (int, error) {
	n, ok := ceilings[c]
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownClass, c)
	}
	return n, nil
}

// Named refusals. Each pairs with a zero Manifest — none is ever absorbed into a degraded
// result — and each states, via shape(), the fields a corrected return must carry, so the cost
// of a rejection is one re-dispatch rather than a transcript search.
var (
	// ErrUnknownClass reports a ReturnClass with no declared ceiling — a caller naming a class
	// this package has never heard of, not a return that failed validation.
	ErrUnknownClass = fmt.Errorf("boundary: no ceiling declared for this return class")
	// ErrOverLength reports a return whose raw byte length exceeds its class's declared
	// ceiling. It is refused before it is parsed: an over-length return is never given the
	// chance to look well-formed.
	ErrOverLength = fmt.Errorf("boundary: return exceeds its declared length ceiling")
	// ErrNonConforming reports a return that parses, or fails to parse, as anything other than
	// exactly the declared manifest shape: a missing required field, an undeclared extra
	// field, trailing content, or a facts list past its bound.
	ErrNonConforming = fmt.Errorf("boundary: return does not conform to the bounded-manifest shape")
)

// Manifest is the bounded return every dispatch is held to. It is the only shape Validate ever
// produces — there is no richer variant that carries deliverable content, so a caller can never
// receive by accident what it is never sent on purpose.
type Manifest struct {
	// Status is required. For a deliverable manifest it is the run's own pass/fail; for a
	// control-plane verdict it is the verdict itself (e.g. "pass", "fix") — the harness's own
	// declared vocabulary governs which values are meaningful, not this package.
	Status string `json:"status"`
	// ArtifactPaths names where the deliverable actually lives. It is the only route back to
	// deliverable bytes a Manifest carries; this package never opens a path it names.
	ArtifactPaths []string `json:"artifact_paths,omitempty"`
	// Facts is a FEW key facts, bounded by maxFacts.
	Facts []string `json:"facts,omitempty"`
	// NextAction is required: what happens next, as this run's own stated claim.
	NextAction string `json:"next_action"`
}

// shape describes the declared manifest fields for use inside a rejection's message, so the
// message alone tells a caller exactly what a corrected return must carry.
func shape() string {
	return fmt.Sprintf(
		"{status (string, required), artifact_paths ([]string, optional), facts ([]string, optional, max %d), next_action (string, required)} — no other field",
		maxFacts,
	)
}

// Validate checks raw against class's declared ceiling and the Manifest shape, in that order —
// an over-length return is refused before it is parsed, so a run that dumped its full
// deliverable as the message never gets the benefit of looking well-formed.
//
// A conforming return decodes into the returned Manifest. Every other outcome is a zero
// Manifest paired with a named error (ErrOverLength or ErrNonConforming, matchable via
// errors.Is), never a partially-filled Manifest and never a degraded placeholder.
func Validate(class ReturnClass, raw []byte) (Manifest, error) {
	ceiling, err := class.Ceiling()
	if err != nil {
		return Manifest{}, err
	}
	if n := len(raw); n > ceiling {
		return Manifest{}, fmt.Errorf("%w: %s return is %d bytes, over its %d-byte ceiling; expected shape %s",
			ErrOverLength, class, n, ceiling, shape())
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %s return %v; expected shape %s", ErrNonConforming, class, err, shape())
	}
	if dec.More() {
		return Manifest{}, fmt.Errorf("%w: %s return carries trailing content after one manifest object; expected shape %s",
			ErrNonConforming, class, shape())
	}
	if strings.TrimSpace(m.Status) == "" {
		return Manifest{}, fmt.Errorf("%w: %s return has no status; expected shape %s", ErrNonConforming, class, shape())
	}
	if strings.TrimSpace(m.NextAction) == "" {
		return Manifest{}, fmt.Errorf("%w: %s return has no next_action; expected shape %s", ErrNonConforming, class, shape())
	}
	if len(m.Facts) > maxFacts {
		return Manifest{}, fmt.Errorf("%w: %s return carries %d facts, over the %d-fact limit; expected shape %s",
			ErrNonConforming, class, len(m.Facts), maxFacts, shape())
	}
	return m, nil
}
