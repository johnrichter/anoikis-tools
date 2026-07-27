// Package cliout turns this engine's outcomes into the one normalized record
// every command writes to stdout, and the exit code that pairs with it.
//
// It exists so no command builds a result record by hand: the mapping from an
// engine directive or a failure to a status class is decided once, here, and
// every command routes through it.
package cliout

import (
	"errors"
	"fmt"
	"strings"

	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/policy"
	"github.com/johnrichter/anoikis-tools/internal/vcs"
	"github.com/johnrichter/claude-shared-tooling/go/clikit"
)

// Tool is this CLI's own name: command[0] of every record, and the name every
// emitted self-invocation carries.
const Tool = "anoikis"

// maxLine is the longest single-line message a diagnostic may carry.
const maxLine = 4096

// OneLine collapses a multi-line message into the single bounded line a
// diagnostic requires, so an underlying error's own formatting never
// invalidates the record carrying it.
func OneLine(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if s == "" {
		return "unspecified error"
	}
	if len(s) > maxLine {
		return s[:maxLine-3] + "..."
	}
	return s
}

// StatusFor maps a directive to the outcome class it reports as.
//
// A halt is an expected negative — the engine asked whether the build may
// continue and the answer is no — so it is gate-negative, not an error.
// A pause is a qualified success: the build is fine and simply has nothing to
// do right now.
func StatusFor(d engine.Directive) clikit.Status {
	switch d.Action {
	case engine.ActionHalt:
		return clikit.StatusGateNegative
	case engine.ActionPause:
		return clikit.StatusCaveats
	default:
		return clikit.StatusSuccess
	}
}

// Directive builds the record for one step outcome.
func Directive(command []string, d engine.Directive, data map[string]any) (*clikit.Result, error) {
	switch StatusFor(d) {
	case clikit.StatusGateNegative:
		diag, err := clikit.NewError(
			"gate_negative.engine."+codeSuffix(string(d.Cause)),
			OneLine(fmt.Sprintf("build halted (%s): %s", d.Cause, d.Reason)),
			clikit.Manual("resolve the named condition, then run `"+Tool+" step` again"),
			subjects(d.Subjects),
		)
		if err != nil {
			return nil, err
		}
		return clikit.NewGateNegative(command, data, []clikit.Diagnostic{diag}, nil)
	case clikit.StatusCaveats:
		diag, err := clikit.NewCaveat(
			"caveats.engine."+codeSuffix(string(d.Cause)),
			OneLine(fmt.Sprintf("build paused (%s): %s", d.Cause, d.Reason)),
			clikit.Reinvoke(Tool, "step"),
			subjects(d.Subjects),
		)
		if err != nil {
			return nil, err
		}
		return clikit.NewCaveats(command, data, []clikit.Diagnostic{diag})
	default:
		return clikit.NewSuccess(command, data)
	}
}

// Failure builds the record for an error a command could not complete
// through.
//
// The status class is chosen from the error itself, never from a default:
// a contract violation in an artifact or a policy is a precondition the
// engine will not proceed without, a missing artifact is not-found, and
// anything unrecognised is internal rather than being flattened into a
// generic failure that hides which layer broke.
func Failure(command []string, subsystem string, err error) (*clikit.Result, error) {
	var self selfDescribing
	if errors.As(err, &self) {
		diag, buildErr := self.Diagnostic()
		if buildErr != nil {
			return nil, buildErr
		}
		return record(command, self.Status(), []clikit.Diagnostic{diag})
	}
	status, code, triage := classify(subsystem, err)
	diag, buildErr := clikit.NewError(code, OneLine(err.Error()), triage, nil)
	if buildErr != nil {
		return nil, buildErr
	}
	return record(command, status, []clikit.Diagnostic{diag})
}

// selfDescribing is an error that names its own outcome class and renders its
// own diagnostic. A subsystem raising one has already decided both — an unmet
// operator precondition is not an internal failure, and the triage that clears
// it is specific — so the mapping below leaves them alone rather than
// re-deriving them from the error's type.
type selfDescribing interface {
	error
	Status() clikit.Status
	Diagnostic() (clikit.Diagnostic, error)
}

// record builds the result for one outcome class and its diagnostics.
func record(command []string, status clikit.Status, errs []clikit.Diagnostic) (*clikit.Result, error) {
	switch status {
	case clikit.StatusPreconditionUnmet:
		return clikit.NewPreconditionUnmet(command, nil, errs, nil)
	case clikit.StatusNotFound:
		return clikit.NewNotFound(command, nil, errs, nil)
	case clikit.StatusConflict:
		return clikit.NewConflict(command, nil, errs, nil)
	case clikit.StatusUsage:
		return clikit.NewUsage(command, nil, errs, nil)
	default:
		return clikit.NewInternal(command, nil, errs, nil)
	}
}

// classify picks the outcome class, diagnostic code and triage for an error.
func classify(subsystem string, err error) (clikit.Status, string, clikit.Triage) {
	var contract *effort.ContractError
	if errors.As(err, &contract) {
		return clikit.StatusPreconditionUnmet,
			"precondition_unmet." + codeSuffix(subsystem) + ".contract_violation",
			clikit.Manual("fix the artifact so it satisfies its schema, then re-run; run `" + Tool + " validate` to see every violation at once")
	}
	var policyContract *policy.ContractError
	if errors.As(err, &policyContract) {
		return clikit.StatusPreconditionUnmet,
			"precondition_unmet." + codeSuffix(subsystem) + ".policy_violation",
			clikit.Manual("fix the harness policy so it satisfies the harness-policy schema, then re-run")
	}
	var precondition *vcs.PreconditionError
	if errors.As(err, &precondition) {
		return clikit.StatusPreconditionUnmet,
			"precondition_unmet." + codeSuffix(subsystem) + ".operator_input_required",
			clikit.Manual("re-run with the missing input; a merge onto the main branch is never completed without it")
	}
	var wrongBranch *vcs.WrongBranchError
	if errors.As(err, &wrongBranch) {
		return clikit.StatusPreconditionUnmet,
			"precondition_unmet." + codeSuffix(subsystem) + ".wrong_branch",
			clikit.RunTool("git", "checkout", wrongBranch.Want)
	}
	var tooLarge *effort.TooLargeError
	if errors.As(err, &tooLarge) {
		return clikit.StatusConflict,
			"conflict." + codeSuffix(subsystem) + ".run_log_line_too_large",
			clikit.Manual("shorten the event's detail or move its payload to an artifact reference, then re-run")
	}
	if errors.Is(err, errPrecondition) {
		return clikit.StatusPreconditionUnmet,
			"precondition_unmet." + codeSuffix(subsystem) + ".order",
			clikit.Manual("satisfy the condition the message names, then re-run")
	}
	if errors.Is(err, errNotFound) {
		return clikit.StatusNotFound, "not_found." + codeSuffix(subsystem) + ".missing", clikit.Manual("create the named subject, then re-run")
	}
	return clikit.StatusInternal, "internal." + codeSuffix(subsystem) + ".failed",
		clikit.Manual("re-run; if this persists, file an issue with the record above")
}

// Sentinels marking the two failure classes a caller can act on directly,
// without every raiser constructing a bespoke type for them.
var (
	errNotFound     = errors.New("not found")
	errPrecondition = errors.New("precondition unmet")
)

// NotFound wraps err as a missing subject, so it reports as not-found rather
// than as an internal failure.
func NotFound(err error) error { return fmt.Errorf("%w: %w", errNotFound, err) }

// Precondition wraps err as a step the caller must take first, so it reports
// as precondition-unmet rather than as an internal failure: nothing went
// wrong, and the state is exactly as it was.
func Precondition(err error) error { return fmt.Errorf("%w: %w", errPrecondition, err) }

// subjects renders a directive's named subjects as diagnostic context.
func subjects(list []string) map[string]any {
	if len(list) == 0 {
		return nil
	}
	return map[string]any{"subjects": list}
}

// codeSuffix renders a name as the lowercase, underscore-joined segment a
// diagnostic code allows.
func codeSuffix(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	if out == "" {
		return "unspecified"
	}
	return out
}
