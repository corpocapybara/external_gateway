# Core rules (shared; read on demand)

The core rules the todo rule (`todo.md`) and the review-lane briefs (`review-lanes.md`) cite by name, carried with
them so a repo clone keeps them (T0096). The global agent instructions point here instead of repeating them. Read
"Tests stay quick" before running tests for a change; read the other two before running review lanes, writing a
critic or verifier brief, or starting a review loop.

## Tests stay quick

**Tests stay quick** (user, 2026-11-10). A change must not make the suite noticeably slower: compare wall time
before and after and report both. Each new test uses the fewest processes, writers, iterations and sleeps that
prove its point; stress runs and measurements go in an opt-in script. A needed slowdown is stated and gets a todo.
**HARD RULE:** run only the tests the change affects; never rerun a suite without a code change in between (noise
is not evidence). Before the merge, run once every test that could exercise a changed file (the tests of the
changed modules and of their callers, found by grep), whatever the risk; the full suite only when the change
touches what every test runs through (a shared core module, test helpers, the test runner) (user, 2026-11-11).
Keep a run's full output in a file (`tee` it to a temp or scratch folder, never only through a filter such as `grep`
or `tail`; a runner that already logs it in full needs none), read a failure from that file, and never rerun a suite
to find a failure (user, 2026-11-23). No code touched (docs, rules, todos): no tests.

**UI work is tested in the real app** (user, 2026-11-11). For a change a user sees in an app's UI, the check in the
running app, started the way its user starts it, over its real backend and data, is part of testing and counts for
more than code tests. Unit tests of the logic do not replace it, nor does a browser harness or a stubbed backend.
If the app cannot be run, the work is not tested: list the check as open and ask the owner.

## Review loops converge

**Review loops converge in two or three rounds** (user, 2026-11-10; T0048/T0054 refinement took 7 rounds, one or
two new findings each, because capped critics reported the first few and stopped):
- Round 1 is a **full pass**: the critic walks every item (each acceptance line, field, test, changed file) and
  lists **every** finding, one line each, tagged **blocking** or **note**. The word cap shortens wording, never
  the list; it ends with "checked: …" naming what it covered.
- **Blocking** = a missing test, an untested branch or an acceptance item with no proof; the author would build
  or ship the wrong thing; an outcome is uncheckable or unproven; or it contradicts the code or an owner decision.
  A review lane's **major** counts as blocking. Everything else is a note: recorded, never a reason for another
  round.
- The fixer fixes the **class**: a finding about one item ("X has no test") is checked against every sibling item.
- Round 2+ is a **verification round** with a **bigger word cap** (user, 2026-11-10): the round-1 model, about 40
  tool calls and 500 words. It gets the finding ledger and the diff, checks
  each fix landed and the changed text adds no blocking defect, and does not re-review unchanged text or reopen
  dispositioned findings.
  A blocking defect it still sees in unchanged text is reported as "missed in round 1".
- No round limit (owner 2026-11-21): while a round is not green, fix and run another round. Only a finding that
  needs an owner decision goes to the owner; keep looping on everything else. From round 4 on, write a
  `REVIEW-LOOP round N: <why it is still not green>` line into the todo note (or the closing report when there is
  no todo), so long loops can be found and investigated later.

## Review-lane map

**Code changes: select review lanes by task risk before committing** (user, 2026-10-26). One brief with request,
acceptance criteria, diff, risk profile, selected lanes, and ≤ 3 key files per lane; build it with
`C:/dev/_tools/scripts/review-board.py <repo> --base <ref> --out <scratchpad>` (unused 11-11). The canonical lanes are
`review-correctness`, `review-scope`, `review-design` and `review-tests` (where each tool keeps them: its own file).
- Docs-only, isolated test-only, and one-line config changes: no formal panel; author evidence plus lightweight
  verification by the primary assistant (architect).
- Trivial, isolated production change: one relevant independent lane.
- **Small todo** (owner 2026-11-23, T0250): size S or smaller, risk low, under 150 changed lines; the S case of
  the next tier, with both lanes in one agent. One refine (and design) critic covering both critic lanes, its
  description saying "refine" (or "design") and the todo id, with a round 2 only after a blocking finding; one
  combined correctness+tests code-review lane (one agent, its description naming both lanes and the todo id); a
  verifier only after a blocking finding. The other tiers apply instead when the change is high risk (the last
  tier; touching another bucket counts), changes agent instructions (AGENTS.md, CLAUDE.md, the shared rules in
  `.ai`), hooks or agent settings, or has 150 changed lines or more.
- Under 150 changed lines and not high risk (below): correctness and tests only (user, 2026-11-05).
- Normal behavior or multi-file change: select relevant lanes; behavior changes normally include correctness and
  tests, add scope for broad diffs/requirement risk, and add design for ownership/interface/structural risk.
- High risk (security, persistence/schema, concurrency, public API, core algorithm, significant performance or
  availability impact, broad architecture/blast radius): all four lanes plus an adversarial challenge; use a
  qualified security reviewer when available.

Reviewers work independently on first pass. High-risk work or material contested findings stay unaccepted until an
independent verifier runs or the authorized owner accepts the residual risk. Use the adversarial challenge only for
high-risk work; with no adversarial role, ask an independent selected reviewer for a separately labeled adversarial
pass. Use a chair for high-risk adjudication or material disagreement; with no chair role, the primary assistant
chairs and records dispositions; no chair accepts for the owner. With no qualified security reviewer, give
correctness and design an explicit security-focused brief, label specialist security coverage UNVERIFIED, and get
owner risk acceptance before accepting a high-risk security change. Verify material findings yourself; prove a "test proves nothing" claim by breaking the
production line, showing the test fails, and reverting. Do not change this lane map based on model tier until
benchmark evidence and the tier-weighting concept have been independently reviewed.
