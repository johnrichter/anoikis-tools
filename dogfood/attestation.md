# Anoikis dogfood attestation — switchover condition (iii)

**Status: ATTESTED.** A real project was taken design -> plan -> execution end-to-end through anoikis-tools' own compiled CLI, driven by genuine, independent Claude Code sessions (never simulated), and merged to `main` with a real signed commit. This closes the third and final switchover condition.

## The 3-condition switchover

| Condition | What it requires | Where it's proven |
|---|---|---|
| (i) released | Tagged, per-OS/arch archives + checksums | M5.P2.T1 |
| (ii) acceptance-green | Every pass2/dag-core-model/v1-gate clause has a passing check | M5.P3.T1, `anoikis-tools/acceptance` |
| (iii) dogfood-attested | >=1 real project driven end-to-end, transcript-attested | **this document** |

With (iii) attested, `anoikis-tools` is switched-to per SC-ANOIKIS-FIRST: every `delivery-agent-team-tools` task that depends transitively on this task is now unblocked.

## The real project

**`runlog-stats`** — a small, standalone Go CLI that reads one anoikis effort's `run-log.jsonl` and prints a per-`node_id`/per-`role` event summary plus a cost total that never folds an unpriced event in as zero. It is genuinely useful (an operator reading a real effort's run log today has no tool for this) and was chosen specifically so `usage.known`/`cost_usd` semantics, the five-value `event` enum, and the accounting-reconciliation habit this repo's own `dag`/`usage` packages practice would all be exercised for real, not invented for the exercise.

It lives in its own fresh repository (`github.com/dogfood/runlog-stats`, module go1.23, stdlib-only), driven through this checkout's compiled `anoikis` binary and this checkout's actual `policy/agents/anoikis-*` roster — never a synthetic stand-in for either. The repository itself is ephemeral (a scratch checkout); every artifact needed to audit the run, plus a full source snapshot of what actually merged, is captured under `evidence/` in this directory so the attestation survives independently of that scratch checkout.

Final signed merge to `main`: commit `ff26a9decb75b202a12dfdf8ae992ac3a1c05556` (`git log --show-signature`: "Good \"git\" signature for john@jrichter.io"). Full source at that commit: `evidence/deliverable-snapshot/`.

## The run, end to end

1. **Design** — `anoikis-architect` (opus, dispatched via a real headless session, no synthetic stand-in) drafted `design.md` from a one-paragraph goal and the repo's actual state. It independently found and used the real `run-log-event.schema.json` on disk rather than trusting the dispatch summary, surfacing a fact the brief omitted (the `event` enum has a fifth value, `grafted`) and recording it as a design decision. `evidence/design.md`.
2. **Plan** — `anoikis-planner` (opus) decomposed the design into `project.json`, `gates.json`, one gate shard, and two node details (`cli-core`: code; `usage-docs`: docs, depends on `cli-core`), then ran this checkout's own `anoikis validate` against what it wrote and reported the result in its return rather than asserting it. `evidence/graph-final/`.
3. **Execution, layer 0** — `cli-core` dispatched to `anoikis-builder`; it built the tool and its own tests, all passing by its own account. `anoikis-tester` then ran an adversarial pass and found a real defect: `bufio.Scanner.Err()` was never checked, so a post-open read failure was silently treated as empty input instead of exiting non-zero with a message. **Real, unplanted failure** (see "the failure and the forced pause" below).
4. **Recovery** — after diagnosing why the failure did not halt the build the way the engine is designed to (see "defects found," below), the node was retried with an explicit `max_attempts: 2`.
5. **Execution, layer 1 (retry)** — a fresh `anoikis-builder` dispatch (branched clean from the original base commit, per the engine's retry contract) fixed the defect and added a regression test distinct from the pre-existing one. `anoikis-tester` re-verified the full acceptance list adversarially and passed it. Recorded, merged, backstop (`go build ./...` over the merged tree) green.
6. **Execution, layer 2** — `usage-docs` dispatched to `anoikis-writer`; it wrote `docs/usage.md` with a worked example verified byte-for-byte against a real run of the built binary, and reconciled `README.md`'s wording. Recorded, merged, backstop green.
7. **Gate review, pass 1** — `anoikis-reviewer` (opus) judged the whole accumulated diff and returned **`fix`**, with two real, independently-verified findings against `usage-docs` (not planted, not guessed — see below). `evidence/reviews/gate-review-1-fix-verdict-findings.json`.
8. **Mechanical graft** — `anoikis close-gate --verdict fix` grafted two fix nodes from the findings, one per reviewed node, exactly as the engine's design states ("nothing judges whether a fix is warranted; the review already did").
9. **Fix cycle** — the `cli-core` fix node's builder independently re-checked the register, found zero findings scoped to `cli-core`, made no edit, and said so rather than inventing a diff to justify one; its tester independently re-verified `cli-core` still holds. The `usage-docs` fix node's writer resolved both real findings for real (added a build instruction it verified itself; corrected a false claim about blank-line counting, verified against a live run). Recorded, merged, backstop green.
10. **Gate review, pass 2** — a fresh `anoikis-reviewer` dispatch re-judged the full diff, explicitly re-verified both prior findings were actually resolved rather than trusting the fix commit's message, and returned **`pass`**.
11. **Gate close + merge to main** — `anoikis close-gate --verdict pass`, then `anoikis merge-gate --confirm ... --resign-base ...` (the one merge target that re-signs every commit and signs the merge commit, per this checkout's own signing policy). Real, verified with `git log --show-signature`.
12. **Stop** — `anoikis step` returned `{"action":"stop","summary":{"gates":1,"nodes":4,"spend":{"cost_usd":7.54358,"known":true}}}`. The effort is complete.

Every session above is a distinct, independent `claude -p` invocation against this checkout's real `policy/agents/*.md` briefs, with a real session id and a real transcript on disk. Full manifest, every transcript path, every session's own billed cost: `evidence/sessions.json`.

## The failure and the forced pause (acceptance criterion 3)

`cli-core`'s first test-stage run genuinely failed (step 3, above) — not a planted fixture, an adversarial tester finding a real gap in the builder's own output. `anoikis record` correctly recorded the node as `failed`. The very next `anoikis step` call returned `{"action":"pause", ...}` rather than proceeding to dispatch anything further: **the build stopped on its own, on a real failure, with no further work admitted past it.** That is the observable behavior criterion 3 asks for, and it held.

The specific *cause* the engine reported (`runs-in-flight`, implying the run was still dispatched) was not the *correct* cause (the node had genuinely failed and, at `max_attempts: 1` by default, had no attempt left — the correct halt is `CauseFailedNode`, "the graph needs an operator replan"). Diagnosing why is the first item below.

## Defects found while dogfooding (real, not invented)

Genuine dogfooding is expected to surface real gaps; here is what this run found, precisely, with evidence. None of these are fixed by this task — `anoikis-tools/dogfood` is this task's declared surface, not the engine's own packages — but each is reported here for a follow-up task to pick up.

1. **`internal/dag/state.go`'s `FoldLog` has no case for `EventFailed`.** Its `switch` only recognizes `EventDispatched` (-> running) and `EventMerged` (-> done). Replaying a `dispatched` then `failed` pair from an unfolded log tail therefore leaves the folded view at `running`: the `dispatched` event sets it, and the `failed` event is silently a no-op in that switch. This is the direct cause of the misreported `runs-in-flight` pause above — confirmed by direct code reading of `internal/dag/state.go` (`FoldLog`) and `internal/dag/model.go` (`Settled()`, which returns `false` for `StatusFailed`, so a failed node is *not* exempt from folding).
2. **`advanceCursor()` (`cmd/build.go`) did not persist `resume-cursor.json` after a `record` call whose batch had nothing mergeable (only a failure).** Confirmed directly: after the first `record` call (which correctly wrote `status: "failed"` to the shard), `find .anoikis/runlog-stats -iname '*cursor*'` found nothing. A second `record` call on the same effort — later, once a passing node existed elsewhere in the batch and needed merging — *did* persist the cursor correctly, so the gap is specific to the no-mergeable-nodes path, not the mechanism as a whole.
3. **The combination is what caused real, on-disk corruption, not just a stale read.** Because no cursor was ever saved, every subsequent state load re-folded the whole log from byte 0, hitting defect 1 every time. When an *idempotent replay* of `record` (the exact recovery the engine's own design calls "simply running the same command again") ran against this bad fold, `engine.Apply`'s already-settled-per-log branch (`case dag.EventFailed: ...; continue`) left the corrupted `running` value in the shard it then wrote back to disk via `SaveShards` — turning a transient read-side bug into a permanent one. Direct operator repair (correcting `graph/cli-and-docs.json` and `graph.json` back to `status: "failed"`, and granting a second attempt) was required to recover; this attestation's evidence bundle preserves the corrupted and repaired states for a fix task to reproduce against.
4. **The findings/review-verdict artifact has no published schema.** `schemas/anoikis/` documents every other artifact an agent or the engine reads or writes (`project`, `gates`, `graph-shard`, `graph-index`, `node`, `run-result`, `run-log-event`, `harness-policy`, `boundary-manifest`) but not the findings ledger the engine's own `findings` package reads (`ledger@1.0.0`, from `ai-shared-lib`'s `ledger` package) nor the review-verdict findings artifact `anoikis-reviewer` writes and `close-gate --findings` consumes. In this run, the reviewer (reasonably, absent a schema) wrote its verdict findings to the same conventional path (`.anoikis/<effort>/findings.json`) that the effort's own findings *register* lives at, and — being a completely different, undocumented shape — silently clobbered it, making every later `step` call fail outright (`declares schema "", this package reads "ledger@1.0.0"`) until repaired. Recommend: publish a findings/ledger schema under `schemas/anoikis/`, and have `anoikis-reviewer`'s brief name a review-verdict artifact path/shape distinct from the effort's own register.

None of these defects are specific to this dogfood project; they would recur on any real effort that hits a genuine test failure or a genuine review fix cycle, which is exactly why exercising both was worth doing here rather than stopping at a clean, all-green run.

## Operator actions taken (for the audit trail)

Every point below is something a human/model driver decided, not something the engine did unattended — recorded here because a dogfood attestation of a driver loop should account for its own driving, not just the engine's output:

- Confirmed the planner's assumed budget ceiling ($25, `enforced_at: gate`) rather than correcting it — the planner flagged it as a carryover requiring operator sign-off, per design.
- Created the `build` branch before the first `step` call (the engine expects it to already exist; nothing in this run's harness policy or scaffolding does that automatically).
- Diagnosed and repaired the shard/index corruption from defect 3 above, and reset the effort's findings register after defect 4, before continuing.
- Granted `cli-core` a second attempt (`max_attempts: 2`) after understanding the real, narrow defect its first attempt's test stage found — the mechanical retry-vs-halt line the engine draws by default (`MaxAttempts` zero means one attempt) is exactly the kind of judgment call the design reserves for an operator.
- Supplied the final `--confirm` message and `--resign-base` for the signed merge to `main` — the one merge every harness policy holds to operator approval and full re-signing.

## Cost and run-log accounting (acceptance criterion 2/3 support)

- **Run log**: `evidence/run-log.jsonl` — every dispatch, completion, failure, merge, and graft this effort produced, append-only, as the engine wrote it.
- **Engine-recorded spend**: `$7.54` (`anoikis step`'s final `stop` summary), priced by this checkout's real `usage.Transcripts` provider reading real session transcripts — never self-reported by an agent.
- **Full real cost of driving this effort**: `$19.93` across the 12 independent real sessions dispatched (1 architect, 1 planner, 5 builder/writer runs, 3 tester runs, 2 reviewer runs) — `evidence/sessions.json`. This exceeds the engine-recorded figure because a multi-stage node's recorded run-result cites one stage's transcript for pricing; the sessions manifest is the complete, non-synthetic total.

## Acceptance

1. >=1 real project driven design -> plan -> execution end-to-end through `anoikis-tools`, transcript-attested — **met**: `runlog-stats`, 12 real sessions, full transcript manifest in `evidence/sessions.json`.
2. Completes the 3-condition switchover; gates every `delivery-agent-team-tools` task — **met**: conditions (i) and (ii) already stood (M5.P2.T1, M5.P3.T1); this document closes (iii).
3. `forces_pause` if the end-to-end dogfood run fails — **met**: a real test-stage failure genuinely stopped the build (`action: pause`) rather than being silently absorbed; see "the failure and the forced pause."
4. Meets PROD-BAR + SC-CODEGOV — **met** for this deliverable's kind: no `anoikis-tools` source was authored or modified by this task (its file surface is `anoikis-tools/dogfood` only); every artifact here is documentation and evidence, accurate against what actually ran, with every path and command in this document resolving against the evidence bundle beside it.
