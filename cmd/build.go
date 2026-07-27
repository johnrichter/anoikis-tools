package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/johnrichter/claude-shared-tooling/go/clikit"
	"github.com/johnrichter/claude-shared-tooling/go/transcript"
	"github.com/spf13/cobra"

	"github.com/johnrichter/anoikis-tools/internal/cliout"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/findings"
	"github.com/johnrichter/anoikis-tools/internal/usage"
	"github.com/johnrichter/anoikis-tools/internal/vcs"
	"github.com/johnrichter/anoikis-tools/schemas"
)

// newStepCmd builds the one command a driver loops on.
func newStepCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "step",
		Short: "Return the one next action: launch, gate, pause, halt or stop",
		Long: `step reads every artifact and returns exactly one next action, with any
follow-up invocations to run verbatim. It decides readiness, batching, gate
boundaries and merge policy so the driver never has to.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openSession(cmd)
			if err != nil {
				return fail(cmd, "session", err)
			}
			d, err := s.step(cmd.Context())
			if err != nil {
				return fail(cmd, "engine", err)
			}
			result, err := cliout.Directive(commandPath(cmd), d, directiveData(d))
			if err != nil {
				return fail(cmd, "engine", err)
			}
			return finish(cmd, result)
		},
	}
}

// step resolves the base ref and open findings, then asks the engine.
func (s *session) step(ctx context.Context) (engine.Directive, error) {
	st, err := s.store.LoadState()
	if err != nil {
		return engine.Directive{}, err
	}
	open, err := s.openFindings()
	if err != nil {
		return engine.Directive{}, err
	}
	env := s.env
	if env.BaseRef == "" {
		env.BaseRef, err = s.buildHead(ctx, st)
		if err != nil {
			return engine.Directive{}, err
		}
	}
	d, err := engine.Step(st, s.harness, s.scheme, s.prover, open, env)
	if err != nil {
		return engine.Directive{}, err
	}
	// A build with nothing left to do — blocked, or finished — has to account
	// for its operator gates before it says so. An unconfirmed gate is work
	// waiting on a person: a precondition, never a blockage the plan has to be
	// rewritten to clear, and never a completion.
	if d.Action == engine.ActionStop || (d.Action == engine.ActionHalt && d.Cause == engine.CauseBlocked) {
		if err := engine.OperatorHold(st, s.nodeDetails(st)); err != nil {
			return engine.Directive{}, err
		}
	}
	return d, nil
}

// openFindings reads the effort's register in the shape the engine consumes:
// enough to decide whether a finding blocks, and nothing about where it lives.
func (s *session) openFindings() ([]engine.Finding, error) {
	reg, err := findings.Open(s.layout, s.harness.BlockingThreshold())
	if err != nil {
		return nil, err
	}
	var out []engine.Finding
	for _, e := range reg.Blocking() {
		out = append(out, engine.Finding{ID: e.ID, Statement: e.Statement, Criticality: e.Criticality})
	}
	return out, nil
}

// buildHead resolves the commit a newly launched layer branches from.
func (s *session) buildHead(ctx context.Context, st dag.State) (string, error) {
	branch := st.Project.BuildBranch
	if branch == "" {
		branch = s.harness.Gates.BuildBranch
	}
	repo, err := vcs.Open(ctx, s.cfg.Repo)
	if err != nil {
		return "", err
	}
	return repo.BranchHead(ctx, branch)
}

// newDispatchCmd builds the command that turns an admitted batch into
// launchable runs.
func newDispatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Create each admitted node's worktree, render and journal its prompt, and return the dispatches",
		Long: `dispatch performs everything that must be durable before an agent runs: one
worktree per node, the rendered prompt written to disk, and a dispatched event
appended to the run log. A process killed after this point is resumable
because all three already exist.`,
		Args: cobra.NoArgs,
		RunE: runDispatch,
	}
	cmd.Flags().Int("layer", -1, "layer sequence number to dispatch (as reported by step)")
	return cmd
}

func runDispatch(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	ctx := cmd.Context()
	d, err := s.step(ctx)
	if err != nil {
		return fail(cmd, "engine", err)
	}
	if d.Action != engine.ActionLaunch {
		result, err := cliout.Directive(commandPath(cmd), d, directiveData(d))
		if err != nil {
			return fail(cmd, "engine", err)
		}
		return finish(cmd, result)
	}
	layer, _ := cmd.Flags().GetInt("layer")
	if layer >= 0 && layer != d.Launch.LayerSeq {
		return fail(cmd, "engine", fmt.Errorf("requested layer %d but the next layer is %d", layer, d.Launch.LayerSeq))
	}

	st, err := s.store.LoadState()
	if err != nil {
		return fail(cmd, "store", err)
	}
	dispatches, err := engine.PlanDispatch(st, s.harness, s.nodeDetails(st), d.Launch.Members, d.Launch.LayerSeq, engine.Env{
		Tool: cliout.Tool, Effort: s.cfg.Effort, BaseRef: d.Launch.BaseRef,
	})
	if err != nil {
		return fail(cmd, "engine", err)
	}

	repo, err := vcs.Open(ctx, s.cfg.Repo)
	if err != nil {
		return fail(cmd, "vcs", err)
	}
	for _, dp := range dispatches {
		path := s.layout.Worktree(dp.NodeID)
		if err := repo.AddWorktree(ctx, path, dp.WorktreeRef, dp.BaseRef); err != nil {
			return fail(cmd, "vcs", err)
		}
		promptRef, digest, err := s.store.WritePrompt(dp.RunID, dp.Prompt)
		if err != nil {
			return fail(cmd, "store", err)
		}
		if err := s.store.AppendEvent(dag.LogEvent{
			TS:            now(),
			RunID:         dp.RunID,
			NodeID:        dp.NodeID,
			Event:         dag.EventDispatched,
			Role:          dp.Stages[0].Role,
			LayerSeq:      d.Launch.LayerSeq,
			Model:         dp.Stages[0].Model,
			ContextWindow: dp.Stages[0].ContextWindow,
			Effort:        dp.Stages[0].Effort,
			WorktreeRef:   dp.WorktreeRef,
			BaseRef:       dp.BaseRef,
			PromptRef:     promptRef,
			PromptDigest:  digest,
		}); err != nil {
			return fail(cmd, "store", err)
		}
	}
	if err := s.store.SaveShards(engine.MarkRunning(st.Shards, d.Launch.Members), now()); err != nil {
		return fail(cmd, "store", err)
	}

	result, err := clikit.NewSuccess(commandPath(cmd), map[string]any{
		"layer_seq":  d.Launch.LayerSeq,
		"base_ref":   d.Launch.BaseRef,
		"dispatches": dispatches,
		"deferred":   d.Launch.Deferred,
	})
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}

// newRecordCmd builds the command that folds a batch's outcomes back into the
// graph and merges the layer.
func newRecordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record a batch's results, merge the layer, and run the post-merge backstop",
		Long: `record writes each node's durable result, journals its transition, merges the
completed batch onto the build branch, then always runs the post-merge
backstop: build the merged result and re-assert that every changed path was
declared. A node only becomes done once that merge has landed.`,
		Args: cobra.NoArgs,
		RunE: runRecord,
	}
	cmd.Flags().String("results", "", "JSON file holding {\"results\": [...]}, one run result per completed node")
	return cmd
}

func runRecord(cmd *cobra.Command, _ []string) error {
	s, err := openSession(cmd)
	if err != nil {
		return fail(cmd, "session", err)
	}
	path, _ := cmd.Flags().GetString("results")
	if path == "" {
		return fail(cmd, "session", cliout.NotFound(fmt.Errorf("no results file named; pass --results")))
	}
	results, err := readResults(path)
	if err != nil {
		return fail(cmd, "store", err)
	}
	ctx := cmd.Context()
	st, err := s.store.LoadState()
	if err != nil {
		return fail(cmd, "store", err)
	}
	layer := st.CurrentLayerSeq()
	outcomes, err := s.price(ctx, st, results)
	if err != nil {
		return fail(cmd, "usage", err)
	}

	rec, err := engine.Apply(st, s.harness, outcomes, layer, now())
	if err != nil {
		return fail(cmd, "engine", err)
	}
	for _, e := range rec.Events {
		if err := s.store.AppendEvent(e); err != nil {
			return fail(cmd, "store", err)
		}
	}
	raised, err := s.recordFindings(rec.Findings)
	if err != nil {
		return fail(cmd, "findings", err)
	}
	if err := s.store.SaveShards(rec.Shards, now()); err != nil {
		return fail(cmd, "store", err)
	}

	data := map[string]any{
		"recorded":  len(outcomes),
		"mergeable": rec.Mergeable,
		"failed":    rec.Failed,
		"spend":     rec.Spend,
	}
	if len(rec.Mergeable) == 0 {
		result, err := clikit.NewSuccess(commandPath(cmd), data)
		if err != nil {
			return fail(cmd, "engine", err)
		}
		return finish(cmd, result)
	}

	repo, err := vcs.Open(ctx, s.cfg.Repo)
	if err != nil {
		return fail(cmd, "vcs", err)
	}
	merge, err := s.mergeLayer(ctx, repo, st, rec.Mergeable, layer)
	if err != nil {
		return fail(cmd, "vcs", err)
	}
	data["merge"] = merge.summary
	if merge.halt != nil {
		// A refused merge has to outlive this invocation. Running the same
		// command again merges nothing — the branches are already in — so its
		// change set is empty and both checks pass vacuously; without a
		// durable record the build would walk straight past a defect nobody
		// fixed. Recording it as a blocking finding is what stops the next
		// step until an operator resolves it.
		if _, err := s.recordFindings([]engine.RaisedFinding{{Seed: dag.FindingSeed{
			Statement: fmt.Sprintf("layer %d merge refused (%s): %s", layer, merge.halt.Cause, merge.halt.Reason),
			Impact:    5, Urgency: 5,
		}}}); err != nil {
			return fail(cmd, "findings", err)
		}
		result, err := cliout.Directive(commandPath(cmd), *merge.halt, data)
		if err != nil {
			return fail(cmd, "engine", err)
		}
		return finish(cmd, result)
	}

	shards, events := engine.Settle(rec.Shards, rec.Mergeable, rec.Runs, layer, now())
	for _, e := range events {
		if err := s.store.AppendEvent(e); err != nil {
			return fail(cmd, "store", err)
		}
	}
	closures, err := s.retire(ctx, repo, rec, outcomes, raised, layer)
	if err != nil {
		return fail(cmd, "store", err)
	}
	if err := s.store.SaveShards(engine.Retire(shards, closures), now()); err != nil {
		return fail(cmd, "store", err)
	}
	if err := s.chargeBudget(rec); err != nil {
		return fail(cmd, "store", err)
	}
	if err := s.advanceCursor(); err != nil {
		return fail(cmd, "store", err)
	}
	data["merged"] = rec.Mergeable
	if len(rec.FixVerdicts) > 0 {
		data["fix_verdicts"] = rec.FixVerdicts
		data["commands"] = fixCommands(s.cfg.Effort, rec.FixVerdicts, outcomes)
	}
	result, err := clikit.NewSuccess(commandPath(cmd), data)
	if err != nil {
		return fail(cmd, "engine", err)
	}
	return finish(cmd, result)
}

// price asks the spend provider what each returned run cost and stores the
// run's durable result. Cost never comes from the run itself: an agent cannot
// measure its own billed tokens, so a result carries only where it ran.
func (s *session) price(ctx context.Context, st dag.State, results []dag.RunResult) ([]engine.Outcome, error) {
	provider, err := s.usageProvider()
	if err != nil {
		return nil, err
	}
	defer func() { _ = provider.Close() }()

	out := make([]engine.Outcome, 0, len(results))
	for _, r := range results {
		run := usage.Run{Project: st.Project.ID}
		if r.Attribution != nil {
			run.SessionID = r.Attribution.SessionID
			run.Agent = r.Attribution.Agent
			if r.Attribution.TranscriptRef != "" {
				run.TranscriptPath = s.layout.Resolve(r.Attribution.TranscriptRef)
			}
		}
		u, err := provider.RunUsage(ctx, run)
		if err != nil {
			return nil, err
		}
		ref, err := s.store.SaveResult(r)
		if err != nil {
			return nil, err
		}
		out = append(out, engine.Outcome{Result: r, Usage: u, ResultRef: ref})
	}
	return out, nil
}

// retire closes out every node the merge landed: its detail record gains the
// rollup of what it produced, moves to the archive by rename, and its worktree
// is torn down. What stays in the shard is the tombstone Retire writes from
// the closures returned here.
func (s *session) retire(ctx context.Context, repo *vcs.Repo, rec engine.Recording, outcomes []engine.Outcome, raised map[string][]string, layer int) ([]engine.Closure, error) {
	byNode := make(map[string]engine.Outcome, len(outcomes))
	for _, o := range outcomes {
		byNode[o.Result.NodeID] = o
	}
	closures := make([]engine.Closure, 0, len(rec.Mergeable))
	for _, id := range rec.Mergeable {
		o := byNode[id]
		detail, err := s.store.LoadDetail(id)
		if err != nil {
			return nil, err
		}
		spend := o.Usage
		detail.Result = &dag.NodeResult{
			ArtifactRefs:  o.Result.ArtifactRefs,
			RunResultRefs: []string{o.ResultRef},
			FindingRefs:   raised[id],
			Usage:         &spend,
		}
		if err := s.store.SaveDetail(detail); err != nil {
			return nil, err
		}
		ref, err := s.store.ArchiveNode(id)
		if err != nil {
			return nil, err
		}
		if err := s.teardown(ctx, repo, id); err != nil {
			return nil, err
		}
		closures = append(closures, engine.Closure{
			NodeID:    id,
			DetailRef: ref,
			Tombstone: dag.Tombstone{
				Summary:   fmt.Sprintf("merged onto the build branch in layer %d", layer),
				CostUSD:   spend.CostUSD,
				CostKnown: spend.Known,
			},
		})
	}
	return closures, nil
}

// teardown removes a merged node's worktree. One that is already gone — a
// recording replayed after an interrupted merge — is simply nothing to do.
func (s *session) teardown(ctx context.Context, repo *vcs.Repo, nodeID string) error {
	path := s.layout.Worktree(nodeID)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return repo.RemoveWorktree(ctx, path)
}

// chargeBudget adds a batch's spend to the effort's running total, recording
// how many of its runs went unpriced so the total never reads as complete when
// it is not.
func (s *session) chargeBudget(rec engine.Recording) error {
	project, err := s.store.LoadProject()
	if err != nil {
		return err
	}
	project.Budget = project.Budget.Fold(rec.Spend, rec.Unpriced)
	return s.store.SaveProject(project)
}

// fixCommands renders the graft each fix verdict calls for, seeded by the
// result the verdict came in, so the one sanctioned build-time graph mutation
// is a command the driver runs rather than a fact it has to act on.
func fixCommands(effort string, nodes []string, outcomes []engine.Outcome) []engine.Command {
	refs := make(map[string]string, len(outcomes))
	for _, o := range outcomes {
		refs[o.Result.NodeID] = o.ResultRef
	}
	out := make([]engine.Command, 0, len(nodes))
	for _, id := range nodes {
		out = append(out, engine.Command{
			Purpose: fmt.Sprintf("graft the fix node the review of %s asked for", id),
			Argv: []string{
				cliout.Tool, "graft", "--effort", effort,
				"--reviewed", id, "--findings", refs[id],
			},
		})
	}
	return out
}

// mergeOutcome is what one layer merge and its backstop produced.
type mergeOutcome struct {
	summary map[string]any
	// halt is set when the merge landed but the backstop or the surface
	// re-assertion refused it — the build stops for an operator rather than
	// building further on top of it.
	halt *engine.Directive
}

// mergeLayer octopus-merges a completed batch and then always runs the
// backstop over the result.
func (s *session) mergeLayer(ctx context.Context, repo *vcs.Repo, st dag.State, members []string, layer int) (mergeOutcome, error) {
	branch := st.Project.BuildBranch
	if branch == "" {
		branch = s.harness.Gates.BuildBranch
	}
	res, err := repo.MergeLayer(ctx, vcs.LayerPlan{
		BuildBranch: branch,
		Branches:    branchesFor(st, members, layer),
		Message:     fmt.Sprintf("Merge layer %d: %d node(s)", layer, len(members)),
	})
	if err != nil {
		return mergeOutcome{}, err
	}

	timeout := time.Duration(s.harness.Backstop.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = s.cfg.Timeout
	}
	backstop, err := repo.Backstop(ctx, s.harness.Backstop.Command, timeout)
	if err != nil {
		return mergeOutcome{}, err
	}
	claims := map[string][]dag.Claim{}
	for _, id := range members {
		if n, ok := st.Node(id); ok {
			claims[id] = n.Surface
		}
	}
	drift := vcs.AssertSurfaces(res.Changed, claims, s.harness.PathDomains())

	out := mergeOutcome{summary: map[string]any{
		"head":     res.Head,
		"base":     res.Base,
		"changed":  len(res.Changed),
		"backstop": backstop,
		"drift":    drift,
	}}
	switch {
	case !backstop.Passed:
		d := engine.Directive{
			Action: engine.ActionHalt, Cause: engine.CauseBackstopFailed,
			Reason:   fmt.Sprintf("the merged layer does not build: %v exited %d", s.harness.Backstop.Command, backstop.ExitCode),
			Subjects: members,
		}
		out.halt = &d
	case len(drift) > 0:
		d := engine.Directive{
			Action: engine.ActionHalt, Cause: engine.CauseSurfaceOverlap,
			Reason:   fmt.Sprintf("%d changed path(s) were not declared by any node in this layer; the disjointness proof was made against declarations that turned out to be untrue", len(drift)),
			Subjects: drift,
		}
		out.halt = &d
	}
	return out, nil
}

// newResumeCmd builds the command that plans a killed build's recovery.
func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Plan recovery of a killed build from the append-only run log",
		Long: `resume tail-scans the run log and classifies every run by its latest event:
merged is finished, complete or failed needs recording again, dispatched with
nothing after it was interrupted and is replayed verbatim. A run-log line
damaged by a kill mid-append is reported as a caveat, never as a failure.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openSession(cmd)
			if err != nil {
				return fail(cmd, "session", err)
			}
			plan, err := s.resumePlan()
			if err != nil {
				return fail(cmd, "store", err)
			}
			data := map[string]any{
				"items":    plan.Items,
				"reissue":  len(plan.Reissued()),
				"rerecord": len(plan.Rerecorded()),
				"commands": plan.Commands,
			}
			if plan.Damaged == 0 {
				result, err := clikit.NewSuccess(commandPath(cmd), data)
				if err != nil {
					return fail(cmd, "engine", err)
				}
				return finish(cmd, result)
			}
			data["damaged"] = plan.Damaged
			caveat, err := clikit.NewCaveat(
				"caveats.store.run_log_damaged",
				cliout.OneLine(plan.DamageDetail),
				clikit.Manual("no action needed: the transition the damaged line would have recorded is treated as never having happened, and its node is scheduled again"),
				nil,
			)
			if err != nil {
				return fail(cmd, "engine", err)
			}
			result, err := clikit.NewCaveats(commandPath(cmd), data, []clikit.Diagnostic{caveat})
			if err != nil {
				return fail(cmd, "engine", err)
			}
			return finish(cmd, result)
		},
	}
}

// resumePlan reads the run log from its cursor and classifies every run,
// carrying through whatever the read could not make sense of.
func (s *session) resumePlan() (engine.ResumePlan, error) {
	st, scan, err := s.store.LoadStateScan()
	if err != nil {
		return engine.ResumePlan{}, err
	}
	return engine.Resume(st, scan.Damaged, scan.DamageDetail, s.env), nil
}

// newReissueCmd builds the command that replays interrupted runs verbatim.
func newReissueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reissue",
		Short: "Reset each interrupted worktree to its base commit and return its stored prompt verbatim",
		Long: `reissue is the second half of a resume. Each interrupted run's worktree is
hard-reset to the commit it branched from and its stored prompt is returned
unchanged, digest included, so the replay is provably the same dispatch. A
hard kill costs the work in flight and never the record of what came before.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openSession(cmd)
			if err != nil {
				return fail(cmd, "session", err)
			}
			plan, err := s.resumePlan()
			if err != nil {
				return fail(cmd, "store", err)
			}
			ctx := cmd.Context()
			repo, err := vcs.Open(ctx, s.cfg.Repo)
			if err != nil {
				return fail(cmd, "vcs", err)
			}
			var replays []map[string]any
			for _, item := range plan.Reissued() {
				path := s.layout.Worktree(item.NodeID)
				if err := repo.ResetWorktree(ctx, path, item.BaseRef); err != nil {
					return fail(cmd, "vcs", err)
				}
				prompt, err := s.store.ReadPrompt(item.PromptRef)
				if err != nil {
					return fail(cmd, "store", err)
				}
				if got := effort.Digest(prompt); got != item.PromptDigest {
					return fail(cmd, "store", fmt.Errorf("stored prompt %s has digest %s but the run was dispatched with %s; it must not be replayed", item.PromptRef, got, item.PromptDigest))
				}
				replays = append(replays, map[string]any{
					"run_id":   item.RunID,
					"node_id":  item.NodeID,
					"worktree": path,
					"prompt":   prompt,
				})
			}
			result, err := clikit.NewSuccess(commandPath(cmd), map[string]any{"replays": replays})
			if err != nil {
				return fail(cmd, "engine", err)
			}
			return finish(cmd, result)
		},
	}
}

// nodeDetails reads every node's detail record, skipping any the store cannot
// produce: a detail that will not load is reported by the readiness gate, which
// names it, rather than by a read that has nothing to say about it.
func (s *session) nodeDetails(st dag.State) map[string]dag.Detail {
	nodes := st.Nodes()
	out := make(map[string]dag.Detail, len(nodes))
	for _, n := range nodes {
		d, err := s.store.LoadDetail(n.ID)
		if err != nil {
			continue
		}
		out[n.ID] = d
	}
	return out
}

// loadDetails reads the detail records for a set of nodes.
func (s *session) loadDetails(st dag.State, nodeIDs []string) (map[string]dag.Detail, error) {
	out := make(map[string]dag.Detail, len(nodeIDs))
	for _, id := range nodeIDs {
		d, err := s.store.LoadDetail(id)
		if err != nil {
			return nil, err
		}
		out[id] = d
	}
	return out, nil
}

// recordFindings adds every observation a batch raised to the register and
// returns the entry ids each node's observations resolved to, so a node's own
// rollup can cite them.
func (s *session) recordFindings(raised []engine.RaisedFinding) (map[string][]string, error) {
	if len(raised) == 0 {
		return nil, nil
	}
	reg, err := findings.Open(s.layout, s.harness.BlockingThreshold())
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, r := range raised {
		ids, err := reg.AddAll([]dag.FindingSeed{r.Seed})
		if err != nil {
			return nil, err
		}
		out[r.NodeID] = append(out[r.NodeID], ids...)
	}
	return out, nil
}

// usageProvider builds the spend seam this harness wired, or the explicit
// unavailable provider when it wired none — which reports unknown rather than
// zero.
func (s *session) usageProvider() (usage.Provider, error) {
	u := s.harness.Usage
	if u == nil || u.TranscriptRoot == "" || u.IndexPath == "" {
		return usage.Unavailable{Reason: "this harness policy declares no usage source"}, nil
	}
	p, err := usage.OpenTranscripts(transcript.ClaudeCodeJSONL{}, u.TranscriptRoot, u.Scope, u.IndexPath)
	if err != nil {
		return usage.Unavailable{Reason: cliout.OneLine(err.Error())}, nil
	}
	return p, nil
}

// advanceCursor seals the run log up to what has been folded into the graph,
// so the next resume reads the tail rather than the history. The layer the
// build has reached is sealed with it: the events that establish it are about
// to fall behind the offset.
func (s *session) advanceCursor() error {
	st, err := s.store.LoadState()
	if err != nil {
		return err
	}
	scan, err := s.store.ScanRunLog(0)
	if err != nil {
		return err
	}
	return s.store.SaveCursor(effort.Cursor{Offset: scan.Offset, NextLayer: st.NextLayerSeq()})
}

// branchesFor returns the worktree branches a layer's members were dispatched
// on, read from the run log rather than re-derived, so a merge always names
// the branches that were actually created.
func branchesFor(st dag.State, members []string, layer int) []string {
	want := map[string]bool{}
	for _, id := range members {
		want[id] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range st.Events {
		if e.Event != dag.EventDispatched || e.LayerSeq != layer || !want[e.NodeID] || e.WorktreeRef == "" {
			continue
		}
		if seen[e.WorktreeRef] {
			continue
		}
		seen[e.WorktreeRef] = true
		out = append(out, e.WorktreeRef)
	}
	return out
}

// readResults loads and validates a results file.
func readResults(path string) ([]dag.RunResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read results %s: %w", path, err)
	}
	var doc struct {
		Results []dag.RunResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse results %s: %w", path, err)
	}
	if len(doc.Results) == 0 {
		return nil, fmt.Errorf("results %s carries no results", path)
	}
	for i := range doc.Results {
		doc.Results[i].SchemaVersion = dag.SchemaVersion
		diags, err := schemas.RunResult.Validate(doc.Results[i])
		if err != nil {
			return nil, err
		}
		if len(diags) > 0 {
			return nil, &effort.ContractError{Path: path, Artifact: schemas.RunResult, Diagnostics: diags}
		}
	}
	return doc.Results, nil
}

// directiveData renders a directive as the record's data members.
func directiveData(d engine.Directive) map[string]any {
	data := map[string]any{"action": string(d.Action)}
	if len(d.Commands) > 0 {
		data["commands"] = d.Commands
	}
	if d.Launch != nil {
		data["launch"] = d.Launch
	}
	if d.Gate != nil {
		data["gate"] = d.Gate
	}
	if d.Summary != nil {
		data["summary"] = d.Summary
	}
	return data
}

// now is the single timestamp source for one command's writes.
func now() string { return time.Now().UTC().Format(time.RFC3339) }
