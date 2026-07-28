package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/johnrichter/claude-shared-tooling/go/bandcheck"
	"github.com/johnrichter/claude-shared-tooling/go/clikit"
	"github.com/johnrichter/claude-shared-tooling/go/gate"
	"github.com/johnrichter/claude-shared-tooling/go/retrieve"
	"github.com/johnrichter/claude-shared-tooling/go/roster"
	"github.com/johnrichter/claude-shared-tooling/go/transcript"
	"github.com/spf13/cobra"

	"github.com/johnrichter/anoikis-tools/internal/cliout"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/findings"
)

// newValidateCmd builds the readiness gate.
func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check an effort is ready to build",
		Long: `validate reports every reason an effort cannot be built, in one pass: cycles,
dangling edges, ids the effort's own scheme rejects, gates a shard names but
the policy does not declare, nodes with no route, stages with no model, and
surfaces nothing can prove disjoint. A plan is fixed once rather than one
failure at a time.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openSession(cmd)
			if err != nil {
				return fail(cmd, "session", err)
			}
			st, err := s.store.LoadState()
			if err != nil {
				return fail(cmd, "store", err)
			}
			rep, err := engine.Validate(st, s.harness, s.scheme, s.nodeDetails(st))
			if err != nil {
				return fail(cmd, "engine", err)
			}
			data := map[string]any{
				"nodes":       rep.Nodes,
				"gates":       rep.Gates,
				"unbatchable": rep.Unbatchable,
				"problems":    rep.Problems,
			}
			if rep.OK() {
				result, err := clikit.NewSuccess(commandPath(cmd), data)
				if err != nil {
					return fail(cmd, "engine", err)
				}
				return finish(cmd, result)
			}
			diags := make([]clikit.Diagnostic, 0, len(rep.Problems))
			for _, p := range rep.Problems {
				d, err := clikit.NewError(
					"gate_negative.plan."+strings.ReplaceAll(p.Code, ".", "_"),
					cliout.OneLine(p.Message),
					clikit.Manual("correct the plan artifact this names, then run `"+cliout.Tool+" validate` again"),
					nil,
				)
				if err != nil {
					return fail(cmd, "engine", err)
				}
				diags = append(diags, d)
				if len(diags) == 50 {
					break
				}
			}
			result, err := clikit.NewGateNegative(commandPath(cmd), data, diags, nil)
			if err != nil {
				return fail(cmd, "engine", err)
			}
			return finish(cmd, result)
		},
	}
}

// newShowCmd builds the level-of-detail read over the graph.
func newShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Project the graph at one level of detail: outline, gate, node or field",
		Long: `show never materializes the whole graph. Each call re-projects from the
artifacts on disk at the granularity asked for, so a driver's context holds
the index and pointers rather than every node's detail.`,
		Args: cobra.NoArgs,
		RunE: runShow,
	}
	cmd.Flags().String("level", string(retrieve.LevelOutline), "outline | group | item | field")
	cmd.Flags().String("id", "", "gate id at group level, node id at item or field level")
	cmd.Flags().String("field", "", "field name, at field level")
	return cmd
}

func runShow(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	levelFlag, _ := cmd.Flags().GetString("level")
	level := retrieve.Level(levelFlag)
	if !level.Valid() {
		return fail(cmd, "session", fmt.Errorf("unknown level %q; one of outline, group, item, field", levelFlag))
	}
	id, _ := cmd.Flags().GetString("id")
	field, _ := cmd.Flags().GetString("field")

	st, err := s.store.LoadState()
	if err != nil {
		return fail(cmd, "store", err)
	}
	view, err := retrieve.Retrieve(st, graphTaxonomy(), level, id, field)
	if err != nil {
		return fail(cmd, "engine", cliout.NotFound(err))
	}
	result, err := clikit.NewSuccess(commandPath(cmd), map[string]any{"level": string(level), "view": view})
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}

// graphTaxonomy declares the graph's shape once: gate shards are the groups,
// nodes are the items. Every projection level is driven off these functions,
// so a new node field never needs a matching case anywhere.
func graphTaxonomy() retrieve.Taxonomy[dag.State, dag.Shard, dag.Node] {
	return retrieve.Taxonomy[dag.State, dag.Shard, dag.Node]{
		Groups:    func(st dag.State) []dag.Shard { return st.Shards },
		GroupID:   func(sh dag.Shard) string { return sh.GateID },
		GroupName: func(sh dag.Shard) string { return sh.GateID },
		Items:     func(sh dag.Shard) []dag.Node { return sh.Nodes },
		ItemID:    func(n dag.Node) string { return n.ID },
		ItemName:  func(n dag.Node) string { return n.Title },
	}
}

// newFindingsCmd builds the ranked feedback register's command group.
func newFindingsCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "findings",
		Short: "Record, rank and fold the effort's deferred findings",
		Long: `findings is the effort's ranked feedback register. Criticality is derived from
impact and urgency by the register, never supplied; the blocking threshold is
the harness policy's. Folding a version resolves every deferred finding as
carried and returns the carryover lines the manifest keeps.`,
	}

	add := &cobra.Command{
		Use:   "add",
		Short: "Record a finding",
		Long: `add appends one observation to the register and derives its criticality from
the impact and urgency given, never from a value supplied directly. The
returned criticality is compared against the harness policy's blocking
threshold so the caller learns immediately whether this finding blocks.`,
		Args: cobra.NoArgs,
		RunE: runFindingsAdd,
	}
	add.Flags().String("statement", "", "what was observed")
	add.Flags().Int("impact", 0, "how much it matters, 1-5")
	add.Flags().Int("urgency", 0, "how soon it must be acted on, 1-5")

	list := &cobra.Command{
		Use:   "list",
		Short: "List findings, criticality-ranked and split at the blocking threshold",
		Long: `list reads the whole register and reports it already split into blocking and
deferred sets against the harness policy's threshold, plus the total count, so
a driver never has to re-derive the split itself.`,
		Args: cobra.NoArgs,
		RunE: runFindingsList,
	}

	fold := &cobra.Command{
		Use:   "fold",
		Short: "Carry every deferred finding into the manifest's carryover and close the version",
		Long: `fold closes out a version's register: every finding still deferred is
resolved as carried, its carryover line is appended to the project manifest,
and the fold is recorded in provenance so a later read can see which findings
crossed a version boundary rather than being acted on.`,
		Args: cobra.NoArgs,
		RunE: runFindingsFold,
	}

	group.AddCommand(add, list, fold)
	return group
}

func runFindingsAdd(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	statement, _ := cmd.Flags().GetString("statement")
	impact, _ := cmd.Flags().GetInt("impact")
	urgency, _ := cmd.Flags().GetInt("urgency")
	reg, err := findings.Open(s.layout, s.harness.BlockingThreshold())
	if err != nil {
		return fail(cmd, "findings", err)
	}
	entry, err := reg.Add(dag.FindingSeed{Statement: statement, Impact: impact, Urgency: urgency})
	if err != nil {
		return fail(cmd, "findings", err)
	}
	result, err := clikit.NewSuccess(commandPath(cmd), map[string]any{
		"id":          entry.ID,
		"criticality": entry.Criticality,
		"blocking":    entry.Criticality >= s.harness.BlockingThreshold(),
	})
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}

func runFindingsList(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	reg, err := findings.Open(s.layout, s.harness.BlockingThreshold())
	if err != nil {
		return fail(cmd, "findings", err)
	}
	result, err := clikit.NewSuccess(commandPath(cmd), map[string]any{
		"threshold": s.harness.BlockingThreshold(),
		"blocking":  reg.Blocking(),
		"deferred":  reg.Deferred(),
		"total":     len(reg.List()),
	})
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}

func runFindingsFold(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	project, err := s.store.LoadProject()
	if err != nil {
		return fail(cmd, "store", err)
	}
	reg, err := findings.Open(s.layout, s.harness.BlockingThreshold())
	if err != nil {
		return fail(cmd, "findings", err)
	}
	lines, folded, err := reg.Fold(project.Version)
	if err != nil {
		return fail(cmd, "findings", err)
	}
	project.Carryover = append(project.Carryover, lines...)
	if project.Provenance == nil {
		project.Provenance = &dag.Provenance{}
	}
	project.Provenance.FoldedFindings = append(project.Provenance.FoldedFindings, folded...)
	if err := s.store.SaveProject(project); err != nil {
		return fail(cmd, "store", err)
	}
	result, err := clikit.NewSuccess(commandPath(cmd), map[string]any{
		"version":   project.Version,
		"folded":    folded,
		"carryover": len(project.Carryover),
	})
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}

// newSelfCheckCmd builds the session tier check.
func newSelfCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "self-check",
		Short: "Check the driving session is inside the tier band its harness declares",
		Long: `self-check resolves the driving session's live model from its own transcript —
only from lines attributable to the orchestrator, never a subagent's — and
compares it against the harness policy's declared band. A model the roster
cannot place is its own outcome, never a below-floor guess.`,
		Args: cobra.NoArgs,
		RunE: runSelfCheck,
	}
	cmd.Flags().String("transcript", "", "session transcript to read the live model from")
	cmd.Flags().String("effort", "", "observed effort level; omitted checks the model axis only")
	return cmd
}

func runSelfCheck(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	if s.harness.Tier == nil {
		return fail(cmd, "session", cliout.NotFound(fmt.Errorf("this harness policy declares no tier band to check against")))
	}
	path, _ := cmd.Flags().GetString("transcript")
	if path == "" {
		return fail(cmd, "session", cliout.NotFound(fmt.Errorf("no transcript named; pass --transcript")))
	}
	f, err := os.Open(path)
	if err != nil {
		return fail(cmd, "usage", err)
	}
	defer func() { _ = f.Close() }()

	model, ok, err := bandcheck.DetectSessionModel(transcript.ClaudeCodeJSONL{}, f)
	if err != nil {
		return fail(cmd, "usage", err)
	}
	if !ok {
		return fail(cmd, "usage", cliout.NotFound(fmt.Errorf("no orchestrator-authored line in %s names a model", path)))
	}
	effortFlag, _ := cmd.Flags().GetString("effort")
	verdict := bandcheck.SelfCheck(model, roster.Effort(effortFlag), effortFlag != "", bandcheck.TierBand{
		FloorModel:    s.harness.Tier.FloorModel,
		FloorEffort:   roster.Effort(s.harness.Tier.FloorEffort),
		CeilingModel:  s.harness.Tier.CeilingModel,
		CeilingEffort: roster.Effort(s.harness.Tier.CeilingEffort),
	})
	data := map[string]any{"model": verdict.Model, "verdict": verdict.VerdictName, "reason": verdict.Reason}
	if verdict.RosterStale {
		data["roster_stale"] = true
	}
	if verdict.Verdict == gate.VerdictAbort {
		diag, err := clikit.NewError(
			"gate_negative.tier.below_floor",
			cliout.OneLine(verdict.Reason),
			clikit.Manual("restart the session at or above the harness policy's declared floor tier"),
			nil,
		)
		if err != nil {
			return fail(cmd, "engine", err)
		}
		result, err := clikit.NewGateNegative(commandPath(cmd), data, []clikit.Diagnostic{diag}, nil)
		if err != nil {
			return fail(cmd, "engine", err)
		}
		return finish(cmd, result)
	}
	result, err := clikit.NewSuccess(commandPath(cmd), data)
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}
