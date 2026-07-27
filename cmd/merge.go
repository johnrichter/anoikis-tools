package cmd

import (
	"fmt"
	"slices"

	"github.com/johnrichter/claude-shared-tooling/go/clikit"
	"github.com/spf13/cobra"

	"github.com/johnrichter/anoikis-tools/internal/cliout"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/vcs"
)

// newMergeGateCmd builds the gate merge — the only command that may target
// the main branch.
func newMergeGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "merge-gate",
		Short: "Merge the build branch onto a gate's target",
		Long: `merge-gate moves the build branch onto the target a gate declares. Targeting
the harness's main branch is the only path that re-signs every commit, signs
the merge commit, and requires an operator-approved message; every other
target merges autonomously and unsigned. The main branch therefore only ever
receives a reviewed, fully signed merge.`,
		Args: cobra.NoArgs,
		RunE: runMergeGate,
	}
	cmd.Flags().String("gate", "", "gate id to merge")
	cmd.Flags().String("confirm", "", "operator-approved merge message; required when the target is the main branch")
	cmd.Flags().String("resign-base", "", "commit bounding the re-signing range; required when the target is the main branch")
	return cmd
}

func runMergeGate(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	gateID, _ := cmd.Flags().GetString("gate")
	if gateID == "" {
		return fail(cmd, "session", cliout.NotFound(fmt.Errorf("no gate named; pass --gate")))
	}
	st, err := s.store.LoadState()
	if err != nil {
		return fail(cmd, "store", err)
	}
	gate, ok := st.Gates.Find(gateID)
	if !ok {
		return fail(cmd, "store", cliout.NotFound(fmt.Errorf("no gate %q in this effort", gateID)))
	}
	if gate.Policy.MergeTarget == dag.MergeTargetNone {
		return fail(cmd, "engine", fmt.Errorf("gate %s declares no merge target", gateID))
	}
	if gate.NeedsReview() && gate.Status != dag.GatePassed {
		// A reviewed gate merges only after its verdict has been fed back.
		// Enforcing the order here is what makes "a reviewed merge" a property
		// of the code rather than of the sequence a driver happened to run.
		return fail(cmd, "engine", cliout.Precondition(fmt.Errorf(
			"gate %s declares a %s deep review and stands at %s; feed the review's verdict back with `%s close-gate` before merging",
			gateID, gate.Policy.DeepReview, gate.Status, cliout.Tool)))
	}

	// A boundary merge is what makes work permanent, so it is the last point at
	// which an operator gate can still be accounted for. One whose confirmation
	// is absent or incomplete holds the merge, whatever status the graph has
	// come to record for its node.
	if err := engine.OperatorHold(st, s.nodeDetails(st)); err != nil {
		return fail(cmd, "engine", err)
	}

	branch := st.Project.BuildBranch
	if branch == "" {
		branch = s.harness.Gates.BuildBranch
	}
	confirm, _ := cmd.Flags().GetString("confirm")
	resignBase, _ := cmd.Flags().GetString("resign-base")
	if resignBase == "" {
		resignBase = st.Project.BaseRef
	}
	targetsMain := s.harness.TargetsMain(gate.Policy.MergeTarget)

	ctx := cmd.Context()
	repo, err := vcs.Open(ctx, s.cfg.Repo)
	if err != nil {
		return fail(cmd, "vcs", err)
	}
	res, err := repo.MergeGate(ctx, vcs.GatePlan{
		BuildBranch: branch,
		Target:      gate.Policy.MergeTarget,
		TargetsMain: targetsMain,
		Message:     confirm,
		ResignBase:  resignBase,
	})
	if err != nil {
		return fail(cmd, "vcs", err)
	}

	for i, g := range st.Gates.Gates {
		if g.ID == gateID {
			st.Gates.Gates[i].Status = dag.GateMerged
		}
	}
	if err := s.store.SaveGates(st.Gates); err != nil {
		return fail(cmd, "store", err)
	}
	result, err := clikit.NewSuccess(commandPath(cmd), map[string]any{
		"gate":         gateID,
		"target":       gate.Policy.MergeTarget,
		"targets_main": targetsMain,
		"head":         res.Head,
		"signed":       res.Signed,
	})
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}

// newCloseGateCmd builds the command that feeds a gate's review verdict back.
func newCloseGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close-gate",
		Short: "Feed a gate's review verdict back and let the build past it",
		Long: `close-gate is how a boundary is passed. A gate that declares a deep review
closes only on a verdict from the harness policy's own vocabulary: the fix
verdict grafts the fix work that verdict calls for and leaves the gate open
until it lands, and every other verdict passes the gate. A gate that declares
no review closes without one.`,
		Args: cobra.NoArgs,
		RunE: runCloseGate,
	}
	cmd.Flags().String("gate", "", "gate id to close")
	cmd.Flags().String("verdict", "", "the deep review's structured verdict, from the harness policy's declared vocabulary")
	cmd.Flags().String("findings", "", "reference to the review's findings artifact; required with the fix verdict")
	return cmd
}

func runCloseGate(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	gateID, _ := cmd.Flags().GetString("gate")
	if gateID == "" {
		return fail(cmd, "session", cliout.NotFound(fmt.Errorf("no gate named; pass --gate")))
	}
	st, err := s.store.LoadState()
	if err != nil {
		return fail(cmd, "store", err)
	}
	gate, ok := st.Gates.Find(gateID)
	if !ok {
		return fail(cmd, "store", cliout.NotFound(fmt.Errorf("no gate %q in this effort", gateID)))
	}
	if gate.Status == dag.GateMerged {
		return fail(cmd, "engine", cliout.Precondition(fmt.Errorf("gate %s has already merged; there is nothing left to close", gateID)))
	}

	verdict, _ := cmd.Flags().GetString("verdict")
	findingsRef, _ := cmd.Flags().GetString("findings")
	details, err := s.loadDetails(st, reviewedIn(st, gateID))
	if err != nil {
		return fail(cmd, "store", err)
	}
	closing, err := engine.CloseGate(st, s.harness, s.scheme, details, gate, verdict, findingsRef, now())
	if err != nil {
		return fail(cmd, "engine", cliout.Precondition(err))
	}

	grafted := make([]string, 0, len(closing.Grafts))
	for _, graft := range closing.Grafts {
		if err := s.applyGraft(graft); err != nil {
			return fail(cmd, "store", err)
		}
		grafted = append(grafted, graft.Node.ID)
	}
	for i, g := range st.Gates.Gates {
		if g.ID == gateID {
			st.Gates.Gates[i].Status = closing.Status
		}
	}
	if err := s.store.SaveGates(st.Gates); err != nil {
		return fail(cmd, "store", err)
	}
	result, err := clikit.NewSuccess(commandPath(cmd), map[string]any{
		"gate":     gateID,
		"verdict":  verdict,
		"status":   closing.Status,
		"reviewed": closing.Reviewed,
		"grafted":  grafted,
	})
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}

// reviewedIn returns the nodes a gate's review covers, which are the ones
// whose detail records closing it needs.
func reviewedIn(st dag.State, gateID string) []string {
	var out []string
	for _, sh := range st.Shards {
		if sh.GateID != gateID {
			continue
		}
		for _, n := range sh.Nodes {
			out = append(out, n.ID)
		}
	}
	return out
}

// newGraftCmd builds the one sanctioned build-time graph mutation.
func newGraftCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graft",
		Short: "Insert the fix node a review's fix verdict calls for",
		Long: `graft acts on a review verdict the harness policy declares, inserting a node
that depends on exactly the nodes reviewed, claims the union of their
surfaces, and is seeded by the review's own findings artifact. It is the one
build-time mutation of a graph otherwise decided at plan time, and it is
mechanical: nothing here judges whether a fix is warranted.`,
		Args: cobra.NoArgs,
		RunE: runGraft,
	}
	cmd.Flags().StringSlice("reviewed", nil, "node ids the review covered")
	cmd.Flags().String("findings", "", "reference to the review's findings artifact")
	return cmd
}

func runGraft(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	reviewed, _ := cmd.Flags().GetStringSlice("reviewed")
	findingsRef, _ := cmd.Flags().GetString("findings")
	if len(reviewed) == 0 || findingsRef == "" {
		return fail(cmd, "session", fmt.Errorf("graft needs both --reviewed and --findings"))
	}
	st, err := s.store.LoadState()
	if err != nil {
		return fail(cmd, "store", err)
	}
	details, err := s.loadDetails(st, reviewed)
	if err != nil {
		return fail(cmd, "store", err)
	}
	graft, err := engine.PlanGraft(st, s.harness, s.scheme, details, reviewed, findingsRef, now())
	if err != nil {
		return fail(cmd, "engine", err)
	}
	if err := s.applyGraft(graft); err != nil {
		return fail(cmd, "store", err)
	}
	result, err := clikit.NewSuccess(commandPath(cmd), map[string]any{
		"node":     graft.Node.ID,
		"gate":     graft.GateID,
		"deps":     graft.Node.Deps,
		"findings": findingsRef,
	})
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}

// applyGraft persists one inserted node: its detail record, its place in the
// gate's shard, and the log entry recording the insertion. The shard is read
// back from the store first, so grafting several nodes in one command builds
// on each other rather than overwriting.
func (s *session) applyGraft(graft engine.Graft) error {
	graft.Node.DetailRef = s.layout.Rel(s.layout.Detail(graft.Node.ID))
	if err := s.store.SaveDetail(graft.Detail); err != nil {
		return err
	}
	st, err := s.store.LoadState()
	if err != nil {
		return err
	}
	shards := slices.Clone(st.Shards)
	inserted := false
	for i := range shards {
		if shards[i].GateID != graft.GateID {
			continue
		}
		shards[i].Nodes = append(slices.Clone(shards[i].Nodes), graft.Node)
		inserted = true
	}
	if !inserted {
		return fmt.Errorf("gate %s has no shard to graft into", graft.GateID)
	}
	if err := s.store.SaveShards(shards, now()); err != nil {
		return err
	}
	return s.store.AppendEvent(graft.Event)
}
