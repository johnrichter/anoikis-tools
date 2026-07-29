// Package cmd wires anoikis-tools' command tree onto the engine.
//
// Every command emits exactly one clikit record to stdout and exits with that
// record's code; cobra's own usage and error printing is silenced so it never
// competes with that one record. Commands do no deciding of their own — they
// load state, hand it to the engine, and render whatever directive comes back.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/johnrichter/claude-shared-tooling/go/clikit"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
	"github.com/spf13/cobra"

	"github.com/johnrichter/anoikis-tools/internal/cliout"
	"github.com/johnrichter/anoikis-tools/internal/config"
	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/anoikis-tools/internal/policy"
)

// exitError carries a clikit-derived exit code up through cobra's error
// return path without cobra printing anything itself — the command that
// raised it has already emitted its record.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// newRootCmd builds the command tree.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   cliout.Tool,
		Short: "Harness-agnostic work-graph design, planning and execution engine",
		Long: strings.TrimLeft(`
anoikis drives a project as a graph of work nodes with dependency edges. A
driver calls `+"`step`"+`, runs the one action it names, and calls `+"`step`"+` again;
every readiness, batching, merge and signing decision is made here, in code.

Everything harness-specific — stages, roles, routes, the gate vocabulary,
document mirrors, the resource domains a surface may claim, the post-merge
backstop — is injected as a harness policy file. The engine itself knows none
of it.
`, "\n"),
		Example: strings.TrimLeft(`
  anoikis validate --effort my-effort
  anoikis step --effort my-effort
  anoikis dispatch --effort my-effort --layer 0
  anoikis record --effort my-effort --results outcomes.json
  anoikis resume --effort my-effort
  anoikis reissue --effort my-effort
  anoikis show --effort my-effort --level outline
  anoikis findings add --effort my-effort --statement "flaky retry" --impact 3 --urgency 2
  anoikis findings list --effort my-effort
  anoikis findings fold --effort my-effort
  anoikis self-check --effort my-effort --transcript session.jsonl
  anoikis merge-gate --effort my-effort --gate g1
  anoikis close-gate --effort my-effort --gate g1 --verdict pass
  anoikis graft --effort my-effort --reviewed node-1 --findings findings/f1.json
`, "\n"),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	flags := root.PersistentFlags()
	flags.String("config", "", "path to a YAML config file (flag > env > file > default)")
	flags.String("repo", "", "repository root the effort directory hangs off (default: the repository containing the working directory)")
	flags.String("effort", "", "effort slug under "+effort.DirName+"/")
	flags.String("policy", "", "harness policy file (default: "+config.PolicyFileName+" inside the effort directory)")
	flags.String("base-ref", "", "commit a newly launched layer branches from (default: the build branch head)")
	flags.Duration("timeout", config.DefaultTimeout, "bound on each subprocess the engine runs")

	root.AddCommand(
		newValidateCmd(),
		newStepCmd(),
		newDispatchCmd(),
		newRecordCmd(),
		newResumeCmd(),
		newReissueCmd(),
		newCloseGateCmd(),
		newMergeGateCmd(),
		newGraftCmd(),
		newShowCmd(),
		newFindingsCmd(),
		newSelfCheckCmd(),
	)
	return root
}

// Execute runs the command tree and returns the process exit code.
func Execute() int {
	root := newRootCmd()
	ranCmd, err := root.ExecuteC()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return emitUsageError(ranCmd, err)
}

// session is one command's resolved world: where the effort lives, what its
// harness policy says, and the engine inputs derived from both.
type session struct {
	cfg     *config.Settings
	layout  effort.Layout
	store   *effort.Store
	harness *policy.Harness
	scheme  ids.Scheme
	prover  *graph.Prover
	env     engine.Env
}

// openSession resolves everything a command needs before it can decide
// anything, in the one order that reports the most useful failure first: the
// effort must exist, its harness policy must load and validate, and only then
// is a store built with that policy's mirrors.
func openSession(cmd *cobra.Command) (*session, error) {
	configFile, _ := cmd.Flags().GetString("config")
	cfg, err := config.Load(cmd.Flags(), configFile)
	if err != nil {
		return nil, err
	}
	if cfg.Effort == "" {
		return nil, cliout.NotFound(fmt.Errorf("no effort named; pass --effort or set %sEFFORT", config.EnvPrefix))
	}
	layout, err := effort.Open(cfg.Repo, cfg.Effort)
	if err != nil {
		return nil, cliout.NotFound(err)
	}
	harness, err := policy.Load(cfg.Policy)
	if err != nil {
		return nil, err
	}
	scheme, err := harness.Scheme()
	if err != nil {
		return nil, err
	}
	prover, err := harness.Prover()
	if err != nil {
		return nil, err
	}
	mirrors, err := mirrorTemplates(harness)
	if err != nil {
		return nil, err
	}
	return &session{
		cfg:     cfg,
		layout:  layout,
		store:   effort.New(layout, mirrors),
		harness: harness,
		scheme:  scheme,
		prover:  prover,
		env:     engine.Env{Tool: cliout.Tool, Effort: cfg.Effort, BaseRef: cfg.BaseRef},
	}, nil
}

// mirrorTemplates parses every Markdown mirror the harness declared.
func mirrorTemplates(h *policy.Harness) (map[string]*template.Template, error) {
	out := map[string]*template.Template{}
	for kind := range h.Mirrors {
		tmpl, err := h.MirrorTemplate(kind)
		if err != nil {
			return nil, err
		}
		if tmpl != nil {
			out[kind] = tmpl
		}
	}
	return out, nil
}

// commandPath renders a command's full invocation as the token slice a
// clikit record requires.
func commandPath(cmd *cobra.Command) []string { return strings.Fields(cmd.CommandPath()) }

// finish emits result and turns it into cobra's error-return path.
func finish(cmd *cobra.Command, result *clikit.Result) error {
	if err := clikit.Emit(cmd.OutOrStdout(), result); err != nil {
		return err
	}
	if result.ExitCode == 0 {
		return nil
	}
	return &exitError{code: result.ExitCode}
}

// fail emits the record for an error the command could not complete through.
// subsystem names the layer that raised it, so the diagnostic code says where
// to look.
func fail(cmd *cobra.Command, subsystem string, err error) error {
	result, buildErr := cliout.Failure(commandPath(cmd), subsystem, err)
	if buildErr != nil {
		return buildErr
	}
	return finish(cmd, result)
}

// emitUsageError handles an error cobra raised before any subcommand ran — no
// record has been emitted yet, so this is the one place that builds one for
// that case.
func emitUsageError(cmd *cobra.Command, err error) int {
	diag, buildErr := clikit.NewError(
		"usage.cli.invalid_invocation",
		cliout.OneLine(err.Error()),
		clikit.Manual("run `"+cliout.Tool+" --help` (or `"+cliout.Tool+" <command> --help`) for valid flags and usage"),
		nil,
	)
	if buildErr != nil {
		fmt.Fprintln(os.Stderr, err)
		return clikit.StatusInternal.ExitCode()
	}
	result, buildErr := clikit.NewUsage(commandPath(cmd), nil, []clikit.Diagnostic{diag}, nil)
	if buildErr != nil {
		fmt.Fprintln(os.Stderr, err)
		return clikit.StatusInternal.ExitCode()
	}
	if emitErr := clikit.Emit(os.Stdout, result); emitErr != nil {
		fmt.Fprintln(os.Stderr, emitErr)
	}
	return result.ExitCode
}
