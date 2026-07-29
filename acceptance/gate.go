// Package acceptance is the conformance gate this engine has to clear before
// anything is switched onto it.
//
// The engine was built greenfield against a known-good target that already
// existed on paper: a prior plan's acceptance specification, a core model of
// the work graph and its artifact contracts, and a set of cost and context
// gates for the first version. Building from a specification and conforming to
// it are different claims, and only the second one is checkable — so every
// clause of that target is registered here with a check that either passes or
// names what is wrong with it.
//
// Three rules shape the registry, and they are what keep the gate from being a
// rubber stamp:
//
//   - One clause per requirement, stated in this engine's own vocabulary, with
//     the check's exact assertion recorded beside it. A report that claims more
//     than the check performed is worse than no report.
//   - A requirement the source specification states as a measurement over a
//     real driven build cannot be settled by inspecting a checkout. Those
//     clauses are held to the mechanism that makes the measurement possible and
//     honest, and the measurement itself is carried out of this gate as an open
//     condition naming what discharges it. Nothing is quietly downgraded: the
//     bar a clause was judged at is in the report.
//   - Any clause that is not met pauses. This gate exists because a greenfield
//     rebuild can diverge from its target in ways no single test would notice,
//     and the answer to divergence is an operator, not a retry.
package acceptance

import (
	"cmp"
	"fmt"
	"slices"
)

// Source is the specification a clause comes from.
type Source string

// The three specifications this gate proves conformance to.
const (
	// SourcePriorSpec is the acceptance specification of the plan this engine
	// was rebuilt from.
	SourcePriorSpec Source = "prior-acceptance-spec"
	// SourceCoreModel is the core model of the work graph: its execution loop,
	// artifact contracts and durability rules.
	SourceCoreModel Source = "core-model"
	// SourceV1Gates is the first version's cost and context gates.
	SourceV1Gates Source = "v1-cost-context-gates"
)

// Bar is what a clause is held to here.
type Bar string

// The two bars, and the rule that separates them: a clause is held to the
// build bar unless its source states it as a measurement over a real driven
// build, in which case only its mechanism can be settled from a checkout.
const (
	// BarBuild is settled entirely by this gate: the clause is either true of
	// the checkout and the compiled engine, or it is not.
	BarBuild Bar = "build"
	// BarMechanism is settled here only as far as the mechanism goes — the
	// thing that makes the live measurement possible, honest and loud. The
	// measurement itself leaves this gate as an open condition.
	BarMechanism Bar = "mechanism"
)

// Clause is one requirement of the known-good target and the check that
// settles it.
type Clause struct {
	// ID names the clause. It is stable: a report is compared across runs by
	// it, and an operator decision is recorded against it.
	ID string
	// Source is the specification the requirement comes from.
	Source Source
	// Bar is what the clause is held to here.
	Bar Bar
	// Requires restates the requirement in this engine's vocabulary.
	Requires string
	// Asserts states exactly what the check verifies — no more, so the report
	// never reads as a broader claim than was made.
	Asserts string
	// Measured is what this gate does not settle, and what does. It is set on
	// every mechanism-bar clause and empty on every build-bar one.
	Measured string
	// check returns one message per violation, empty when the clause holds.
	check func(t *Tree) []string
}

// Verdict is the gate's overall answer.
type Verdict string

// The two verdicts. There is no third: a clause that cannot be settled either
// holds at its declared bar or does not.
const (
	// VerdictGreen means every clause holds at its bar.
	VerdictGreen Verdict = "acceptance-green"
	// VerdictPause means at least one clause does not, and an operator has to
	// decide what happens next.
	VerdictPause Verdict = "pause"
)

// Result is one clause's outcome.
type Result struct {
	ID       string `json:"id"`
	Source   Source `json:"source"`
	Bar      Bar    `json:"bar"`
	Requires string `json:"requires"`
	Asserts  string `json:"asserts"`
	Measured string `json:"measured,omitempty"`
	Met      bool   `json:"met"`
	// Violations is why the clause did not hold, one message per problem,
	// ordered so two runs over the same tree report identically.
	Violations []string `json:"violations,omitempty"`
}

// Condition is a measurement this gate deliberately does not make.
type Condition struct {
	Clause      string `json:"clause"`
	Measurement string `json:"measurement"`
}

// Counts summarises a report.
type Counts struct {
	Clauses int `json:"clauses"`
	Met     int `json:"met"`
	Unmet   int `json:"unmet"`
}

// Report is the auditable, clause-by-clause answer.
//
// It carries no timestamp and no absolute path on purpose: the same checkout
// must produce the same bytes, so the committed report can be compared against
// a fresh run to prove it is current.
type Report struct {
	ReportVersion string  `json:"report_version"`
	Verdict       Verdict `json:"verdict"`
	// ForcesPause is the operator-facing consequence of the verdict, stated as
	// its own field so a reader never has to infer it.
	ForcesPause bool   `json:"forces_pause"`
	Action      string `json:"action"`
	Counts      Counts `json:"counts"`
	// BySource counts unmet clauses per specification, so a divergence
	// concentrated in one source is visible without reading every row.
	BySource map[Source]Counts `json:"by_source"`
	// OpenConditions are the live measurements the mechanism-bar clauses leave
	// to whatever drives a real build through this engine.
	OpenConditions []Condition `json:"open_conditions"`
	Clauses        []Result    `json:"clauses"`
}

// ReportVersion is the report's own shape version. It is independent of the
// engine's artifact contracts: this report is a gate's output, not an artifact
// the engine reads.
const ReportVersion = "1.0.0"

// Clauses returns every registered clause, sorted by id.
//
// The three sets are assembled here and nowhere else, so a clause that exists
// is a clause that runs — there is no second list to keep in step.
func Clauses() []Clause {
	var all []Clause
	all = append(all, priorSpecClauses()...)
	all = append(all, coreModelClauses()...)
	all = append(all, v1GateClauses()...)
	slices.SortFunc(all, func(a, b Clause) int { return cmp.Compare(a.ID, b.ID) })
	return all
}

// Run evaluates every clause against the checkout at root.
//
// Nothing here recovers a panicking check. A gate that reported green because
// a check crashed is exactly the failure this package exists to prevent, so a
// broken check takes the run down with it.
func Run(root string) (Report, error) {
	t, err := OpenTree(root)
	if err != nil {
		return Report{}, err
	}
	clauses := Clauses()
	if err := duplicateIDs(clauses); err != nil {
		return Report{}, err
	}

	rep := Report{
		ReportVersion: ReportVersion,
		BySource:      map[Source]Counts{},
	}
	for _, c := range clauses {
		violations := c.check(t)
		res := Result{
			ID:         c.ID,
			Source:     c.Source,
			Bar:        c.Bar,
			Requires:   c.Requires,
			Asserts:    c.Asserts,
			Measured:   c.Measured,
			Met:        len(violations) == 0,
			Violations: violations,
		}
		rep.Clauses = append(rep.Clauses, res)

		counts := rep.BySource[c.Source]
		counts.Clauses++
		rep.Counts.Clauses++
		if res.Met {
			counts.Met++
			rep.Counts.Met++
		} else {
			counts.Unmet++
			rep.Counts.Unmet++
		}
		rep.BySource[c.Source] = counts

		if c.Bar == BarMechanism && c.Measured != "" {
			rep.OpenConditions = append(rep.OpenConditions, Condition{Clause: c.ID, Measurement: c.Measured})
		}
	}

	rep.Verdict = VerdictGreen
	rep.Action = "none: every clause holds at its bar, so the acceptance condition of the switchover is satisfied and the open conditions below pass to the driven build that follows it"
	if rep.Counts.Unmet > 0 {
		rep.Verdict = VerdictPause
		rep.ForcesPause = true
		rep.Action = fmt.Sprintf(
			"pause the switchover: %d of %d clauses did not hold. Each unmet clause below names its violations. An operator decides, per clause, whether the engine is corrected or the divergence is accepted and the target amended — this gate never decides either for them",
			rep.Counts.Unmet, rep.Counts.Clauses)
	}
	return rep, nil
}

// duplicateIDs refuses a registry holding one id twice: two clauses under one
// id would silently overwrite each other in any audit that keys on it.
func duplicateIDs(clauses []Clause) error {
	seen := map[string]bool{}
	for _, c := range clauses {
		if seen[c.ID] {
			return fmt.Errorf("acceptance: clause %q is registered twice", c.ID)
		}
		seen[c.ID] = true
	}
	return nil
}

// Unmet returns the clauses that did not hold, in report order.
func (r Report) Unmet() []Result {
	var out []Result
	for _, c := range r.Clauses {
		if !c.Met {
			out = append(out, c)
		}
	}
	return out
}
