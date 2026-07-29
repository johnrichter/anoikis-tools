# dogfood-evidence fixtures

Byte-for-byte copies of two files from `dogfood/evidence/` (M5.P3.T2's read-only
evidence bundle), checked in here so the regression tests in `build_test.go`
reproduce the actual corruption against the real preserved bytes rather than a
lookalike fixture.

- `run-log-head.jsonl` — the first two lines of `dogfood/evidence/run-log.jsonl`:
  the real `dispatched` then `failed` pair for node `cli-core`, run
  `cli-core-l0-a0`. This is the exact log tail that folded to `running` under
  the pre-fix `FoldLog`.
- `cli-core-test-result.json` — `dogfood/evidence/results/cli-core-test.json`
  verbatim: the real test-stage failure diagnostic for that run. Its bytes
  become the `excerpt` of the `RunResult` the reproduction test feeds to
  `record`, so the failure driving the test is the real one, not invented text.

`dogfood/` itself stays untouched; these are copies, not references into it.
