# anoikis-tools

A deterministic engine for running a project as a graph of work nodes with dependency edges.

A driver — human or model — calls `anoikis step`, runs the one action it names, and calls `step` again. Readiness, batching, gate boundaries, merge and signing policy are all decided here, in code, from state that lives entirely on disk. Identical state yields an identical directive, so a build is repeatable and a killed one is resumable.

## The loop

```
loop:
  d = anoikis step
  case d.action:
    launch: run d.commands verbatim, launch each returned dispatch, then record the results
    gate:   dispatch the review d.gate names, then run d.commands verbatim to feed the
            verdict back and merge
    pause:  surface d.reason; safe exit
    halt:   surface d.reason; an operator is needed
    stop:   the graph is complete
```

Every command a directive emits is an invocation of this CLI, never raw git. The driver runs it; the CLI performs the operation, including checking out whatever branch it needs. Nothing composes a git argument by hand.

## What it does

- **One next directive.** `step` returns exactly one of launch, gate, pause, halt or stop, with the follow-up invocations to run.
- **Surface-disjoint parallel admission.** Two nodes share a batch only when the dependency graph proves them independent *and* a resource-surface prover proves their declared surfaces disjoint. Anything unproven runs alone.
- **An always-on post-merge backstop.** A disjointness proof works over declared text; two nodes adding the same symbol to one package merge cleanly and fail to build. After every layer merge the engine builds the merged result and re-asserts that every changed path was actually declared. It is not a policy choice — a harness policy with no backstop command is refused when it loads.
- **Two merges, kept apart.** A layer merge octopus-merges a finished batch onto the build branch, autonomously and unsigned. A gate merge moves the build branch onto a target; only the declared main branch re-signs every commit, signs the merge commit, and requires an operator-approved message. A gate that declares a review will not merge until that review's verdict has been fed back, so main only ever receives a reviewed, fully signed merge, by construction.
- **A node closes when it merges.** A merged node's detail record gains what it produced, moves to the archive by rename, and leaves a tombstone in its shard so a later node's archived dependency still resolves. Its worktree is torn down with it.
- **Kill-safe resume.** Every dispatch writes its worktree, its rendered prompt and a run-log line before an agent runs. A resume classifies each run by its latest logged event: merged is finished, complete needs recording again, dispatched with nothing after it is replayed verbatim from its stored prompt. A log line damaged by a kill mid-append is a caveat, not a failure.
- **One graph mutation, mechanical.** A review's fix verdict grafts a node that depends on exactly the nodes reviewed, claims the union of their surfaces, and is seeded by the review's findings. Nothing judges whether a fix is warranted; the review already did.
- **Spend that never lies.** What a run cost comes from a spend provider, never from the agent that ran — a run reports only where it executed, and the provider prices it. A run that could not be priced reports unknown with a reason; the effort's own total records how many such runs it holds, so a ceiling is never enforced against a figure that is really a floor.

## Harness-agnostic by construction

The engine holds no knowledge of any particular harness. Three seams carry all of it:

| Seam | Interface | What it decouples |
|---|---|---|
| Session-log access | `transcript.TranscriptSource` behind a `usage.Provider` | Where spend comes from, and what happens when there is no source at all. |
| Node identifiers | `ids.Scheme` | The id grammar. Two schemes ship (`opaque`, `dotted`); a harness registers its own. |
| Everything harness-shaped | an injected harness-policy file | Stages, roles, routes, gate and review vocabulary, document mirrors, resource domains, backstop command, tier band. |

Nothing in the import graph reaches the prior harness this engine replaces, and no name in the tree comes from it. Both are asserted by a test rather than left to intent.

## Effort directories

One directory per effort under `.anoikis/<slug>/`, tracked in git so a build can resume and hand off across sessions.

```
.anoikis/<slug>/
  harness-policy.json      the injected policy
  project.json (+ .md)     manifest: budget, signing, carryover, refs
  graph.json               shard index with status counts
  graph/<gate>.json        one gate's nodes, edges and surfaces
  nodes/<id>.json          node detail, read only on dispatch
  gates.json               per-gate policy and status
  run-log.jsonl            append-only, one line per transition
  resume-cursor.json       how far the log has been folded in, and the layer reached
  findings.json (+ .md)    the ranked findings register
  results/<id>.json        durable run results
  prompts/<run>.txt        verbatim dispatch prompts
  archive/nodes/<id>.json  closed node detail
  logs/ · worktrees/       ephemeral, never committed
```

The contracts behind these files live in [`schemas/anoikis/`](schemas/anoikis/README.md) and are enforced on every read and every write.

## Commands

| Command | Purpose |
|---|---|
| `validate` | The readiness gate: every reason an effort cannot be built, in one pass. |
| `step` | The one next directive. |
| `dispatch` | Create each admitted node's worktree, render and journal its prompt, return the dispatches. |
| `record` | Record a batch's results, merge the layer, run the backstop. |
| `resume` · `reissue` | Plan and perform recovery of a killed build. |
| `close-gate` | Feed a gate's review verdict back and let the build past it. |
| `merge-gate` | Merge the build branch onto a gate's target. |
| `graft` | Insert the fix node a review's fix verdict calls for. |
| `show` | Project the graph at one level of detail. |
| `findings` | Record, rank and fold deferred findings. |
| `self-check` | Check the driving session is inside its declared tier band. |

Every command writes one normalized record to stdout and exits with that record's code, per the shared CLI output contract.

## Harness policy

Injected, never compiled in. [`examples/harness-policy.json`](examples/harness-policy.json) is a working starting point; the contract is `schemas/anoikis/harness-policy.schema.json`.

The path is a declared setting, resolved `flag > env > file > default`, defaulting to `harness-policy.json` inside the effort directory:

```sh
anoikis step --effort my-effort --policy path/to/harness-policy.json
ANOIKIS_EFFORT=my-effort anoikis step
```

One rule is worth knowing before writing a policy: **registering a resource domain obliges every node to claim in it.** A disjointness proof treats an unclaimed domain as unsafe rather than as empty, so a domain nobody claims in serializes the whole build. `validate` counts the nodes this affects and reports them as unbatchable.

## Building

```sh
go build ./...
go test ./...
```

The shared libraries are sibling-repo modules resolved through `replace` directives against `../ai-shared-lib/go/*`, so a checkout expects `ai-shared-lib` beside this repo.

## Claude Code setup

This repo's `.claude/settings.json` enables plugins from the `jr-claude-plugins` marketplace. Register it once at the Claude user level — repo settings carry no machine-specific paths:

```sh
claude plugin marketplace add git@github.com:johnrichter/claude-marketplace.git
# or, with the psa-platform repos checked out as siblings:
claude plugin marketplace add ../marketplace-public
```

Knowledge bases are configured at the Claude user level, not per repo.
