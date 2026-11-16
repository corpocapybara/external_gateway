# ToDo and handovers (repos with `todo/_config.yaml`)

**ToDo** is the tool for open work. It replaced the repos' old to-do list files. It is **not** Claude Code's
Task/TaskCreate tools or any in-session task list: "todo" and "the ToDo tool" mean this tool.
One todo = one file `todo/<ID>.md`. `todo/PRIORITY.md` lists every open todo in the order to take it (priority,
then `order`, then id). Work from the repo's main checkout, never inside a worktree.

**The tool is available** when the `todo` command is on PATH (a shim that runs
`python -I -X utf8 C:/dev/_tools/todo/todo.py`; run it from your own folder, e.g. `todo list`). Then edit todos and
handovers **only** through it (`todo list|show|new|set|move|…`); never by hand or with plain git. `todo <cmd> --help`
prints each command's options and examples; **DESIGN.md**, cited below by section, is the tool's design
(`C:/dev/_tools/todo/DESIGN.md`): what the tool checks, refuses and prints is there, not here.
**CLI edits** (`new set move block unblock subtask`) are pending: written to the working tree, not committed. Run
`todo commit` when your CLI edits are done. Every rule above the last section applies **only while the tool is
running**; **without the tool** see the last section.

**From another repo** only `todo new --repo <repo>` writes (a draft; `todo list --inbox` there shows it; DESIGN.md 15).

## States
`draft` → `refined` → `designed` → `in-progress` → `in-review` → `reviewed` → `in-testing` → `tested` → done.
Each move goes one step. Backward moves are allowed (`in-review` and later → `in-progress`, `reviewed` and later →
`in-review`, `designed` → `refined`); they clear `review` and `test`, and on redesign `design_review`. The retired
`no-design-needed` state takes no new todos (DESIGN.md 3.1).

| State | Means | Needs (cumulative) |
|---|---|---|
| `draft` | Someone wrote it down (a found-work report, the owner, a `TODO` note). A title is enough. | — |
| `refined` | Refinement review done (below); acceptance still open to the design step | non-empty `acceptance`; `refine_review` passed |
| `designed` | Design written and passed the design critics, or no design needed; **acceptance frozen** | `design` link and `design_review` passed; or `design: none` (never `size: L`; above S a move note gives the reason) |
| `in-progress` | Dispatched | `branch`; no live `blockers`; never `size: L` (split it) |
| `in-review` | Review lanes running | — |
| `reviewed` | Review lanes passed | `review` block: `head`, `lanes` |
| `in-testing` | Tests running on the reviewed head | — |
| `tested` | Tests passed on the reviewed head | `test` block: `head` equal to `review.head`, and the run (Test runs) |

### Starting work

**Starting work: `todo assign ID`** (owner 2026-11-11), the first step of any work on a todo in any state, claims it
for this session. Refused: another session holds it: stop and report (an orchestrator skips it). Take a claim over
(`--takeover`) or clear another holder's only on the brief's or the owner's word, or when that session is gone;
"needs --session": carry on without the claim. **When the step is done** (the todo moved on) **or the session stops
working on it**, release it with `todo work clear ID` so the next session can assign it (DESIGN.md 17).

### Refinement

**`draft` → `refined`: the refinement review.** The orchestrator refines the todo through the tool. It may
dispatch refiner agents (**at most 3 per wave**); they return proposed fields and **never write**, and the
orchestrator is the single writer during the wave. Refining means:
1. Set every field: `priority` from the project's goals (`P!` for really important work, at most 5 open per repo,
   DESIGN.md 22; `P4` for trivial / nice-to-have work), `bucket` from the files it touches (replace `unset`), `size`.
   Decide the **risk**: high when it touches a public API, persistence/schema, concurrency or another bucket, else low.
2. Write acceptance as checkable outcomes (an observable behaviour, an exact test or command, a file). Each one
   traces to the todo's problem, and nothing goes beyond it.
3. Check the claim against the code. Already implemented: say so with `file:line`. Duplicate or superseded:
   report it, don't refine it. Work wholly in another project -> `todo move-repo`; work in two projects ->
   `todo split ... --to-repo R`.
4. Make it **one atomic change** (one branch, one merge), even if the feature is still incomplete. Too big: narrow
   it to the first slice and create the others as `draft` todos ordered with `blockers`; an umbrella parent with no
   acceptance of its own is unlinked and reported.
5. Put every uncertainty to the owner as a question. If one cannot wait, record it as a blocker or in the body;
   the todo stays `draft` until it is answered.

Then an **independent critic agent** (never the author) checks the todo. **Hard rule: review until green.** Apply
or reject (with a reason) its findings and send the changed todo to a critic for another round. Only a round that
returns PASS with no open **blocking** finding lets the todo move to `refined`; a written fix is not a pass, notes
go to the design step, and a finding naming a missing test or an unproven acceptance item is blocking. Rounds follow
the core rule "Review loops converge" (`.ai/shared/rules/core.md`): round 1 is a full pass (each acceptance item checked for traceability,
checkability against the code and a named test; stress-style acceptance moves to an opt-in script), later rounds
verify the fixes only. Every critic brief states "round N of max 3" and, from round 2, carries the finding ledger
and the diff (bigger cap: about 40 tool calls, 500 words; a blocking defect in unchanged text is "missed in round
1"). Never start a round 4: open blocking findings after round 3 go to the owner as questions. A round counts only
when the critic ends with `checked: …`; what it did not check goes to a continuation critic in the same round.
Record the move as `todo move ID refined --evidence refine_review.result=passed --evidence refine_review.rounds=N
--note "..."` (refused without it). The note names the rounds, every rejected finding and `risk: high (<reasons>)`
or `risk: low`, and ends with the **design verdict** (owner 2026-11-11): `design: needed (<which point below
holds>)` or `design: not needed (<why none holds>)`, by the no-design test below. When not needed, the refining
session sets `design=none` and moves the todo on to `designed` at once (that move's note repeats the reason).

### Review lanes

**Lanes scale with the refinement's verdict** (trial, owner 2026-11-10). A todo of `size` M or L, or with
`risk: high`, gets two lanes in parallel in round 1 of every review (refinement, design and code review). In
refinement and design they are *code and scope* (every caller by grep, every cited anchor resolved in the current
tree) and *tests and edge cases* (a named test per acceptance item, failure paths, races); in code review it never
takes the core map's (lane map, `.ai/shared/rules/core.md`) one-lane or no-panel tier, and `risk: high` takes its high-risk panel. An S todo with
`risk: low` gets one critic. Round 2+ is one verifier on the round-1 model. Move notes give rounds, lanes and the r2+
finding classes. The code-review lane briefs are `.ai/shared/rules/review-lanes.md` (T0097): run a lane as the
installed `review-<lane>` agent, else as a read-only agent briefed with that lane's section.

### No design

**`refined` → `designed` with `design: none`** (no design needed) right away when none of these holds:
- more than one plausible approach with a real trade-off;
- a new model, formula or derivation to work out;
- a change to a public API, persistence/schema or a cross-bucket interface;
- open owner questions.

Never for `size: L`. Otherwise the todo goes to design.

### Written design

**A written design** is a Markdown file at `docs/design/<ID>-<slug>.md`, linked from the todo's `design` field
(`todo set <ID> design=…` or `--evidence design=…` on the move). Its headings, in order (template: DESIGN.md 18):
`# <ID> design: <title>`, `## Decisions in brief` (at most 2,000 characters, one line per owner decision and frozen
choice, each naming the section that explains it), `## Acceptance` (restated), `## Anchors` (repo-relative file and
symbol, never a line number), `## Decisions` (D1, D2, …), `## Test plan` (one named test per acceptance line),
`## Not changed`. **Commit the design file in the main checkout before you link it** (without git: on disk). The
design critics check `## Decisions in brief` (there, within 2,000 characters, in line with the doc) and resolve each
anchor against the current tree; a line-number or unresolved anchor is blocking.

### Reading a design doc

**Read a design doc by section** (TD0250). Refiners, critics, implementers and the subagents they brief read a
design doc's `## Decisions in brief` first, then only the section a question needs: list the headings with
`grep -n '^## ' <doc>` and read that heading's line range, or grep for the symbol or anchor. Never read a whole
design doc for one fact; DESIGN.md is read the same way. Only writing or reviewing that design reads the whole doc.

### Acceptance freeze

**Acceptance freezes when the todo lands in `designed`** (owner 2026-11-10; a move back to `refined` lifts it).
While `refined`, the design step or no-design check puts every question touching acceptance to the owner, writes
the answers into `acceptance` (the design critics check them), and moves only when none is open.

### Done

Done is not a move target. `todo done --apply` deletes each `tested` todo whose work landed on the done branch (no
`--apply`: a dry run; DESIGN.md 4.3). Work landed in another repo: first `todo set ID work_repo=<repo>`.

## Who moves them
The tools enforce the field and step rules above; the roles below are convention.
- **Orchestrator** (the agent that dispatches work): creates todos, moves them one step at a time, and sets `branch`
  at dispatch.
- **Implementer** (the agent working on the todo: the orchestrator itself or a session it dispatched) **delivers it
  to the end** (owner 2026-11-10), without waiting for anyone: records the review lanes' and test verdicts as
  evidence (the tests run after rebasing onto master), merges the branch to master with `--no-ff` at once (standing
  approval; no re-test on the merge, even if master moved), pushes (when there is a remote), runs
  `todo done --apply`, then removes its worktree and branch; `todo deliver ID` does all of these from `tested`. It
  runs the affected tests on master after the push and fixes a failure forward. Its todo writes go to the main
  checkout (`--repo <main checkout>`), never to a worktree copy. Its worktree goes at
  `<main checkout>/.worktrees/<id>`, never a sibling folder (owner 2026-11-11: outside paths make edits ask).
  A merge, checkout or editor save that rewrites this hard-linked rule file breaks its link (TD0293): run the fix
  command `todo check` prints for a forked copy (`todo deliver`'s links step re-links it too), and re-check the link
  right before reporting, since other sessions re-link.
  Before it changes course on a design point (keeps, revives, removes or reworks a part, interface or approach), it
  quotes the design-doc section or the dated owner decision that settles it; leftover code of a retired part is a
  thing to remove or report, not a decision to revive. When nothing settles it, it asks the owner before reworking.
  It and its lane and subagent briefs read the design's `## Decisions in brief` and only the sections it names.
  Without git there is no branch, merge or push: attach the review verdict and the test output (`todo attach ID
  FILE`), record `review.head=none review.proof=<name>` and `test.head=none test.proof=<name>`, and after `tested`
  run `todo done --apply`. The core map's (`.ai/shared/rules/core.md`) no-panel tier (docs-only, isolated test-only, one-line config) is recorded
  as `review.lanes=none`.
  Each phase is a persisted state (TD0145): `in-review` when the lanes start, `reviewed` when they pass,
  `in-testing` when the tests start, `tested` when they pass, with `todo commit` after each move. A review fix stays
  in `in-review` (re-reviewed on the new head); a test fix moves back to `in-review`; a big change (new approach,
  changed design, a large diff) moves back to `in-progress` with the reason in the move note.
  When a review or test needs the owner's input, set `owner_question=<one line>`, ask in chat and keep working on
  what does not depend on it; once answered, clear it (`owner_question=`) and record the answer in the next move note.
  A todo waiting on something outside the repo gets `external_blocker=<one line>` (DESIGN.md 21); once resolved,
  lift it with `external_blocker=`, naming who lifted it.

  Neither changes `acceptance` from `designed` on, jumps over a state, or writes as human.
- **Review lanes, critics and the subagents an implementer spawns never write todos.** They supply verdicts, and
  the implementer records them.
- **Owner** (human): may change frozen acceptance, jump states, and mark a todo done from the web page.

## Flow rules
`todo assign ID` and `todo show ID` print the project's flow for the todo's bucket (`flow (<bucket|default>,
<phase>):`; DESIGN.md 2.3). Read it at assign and follow it in each phase: `tests` is the kind of test run that
satisfies the test phase (enforced at the move into `tested`; `none`: record `none` in `test.command`/`line`),
`owner_signoff: review` means the owner looks at the output before the review passes (ask the owner), the `note` is
the project's own instruction.

## Removing a todo
A todo that should not exist (empty umbrella, duplicate, superseded) is removed with `todo remove ID --reason "..."`
(DESIGN.md 4.7). From `in-progress` to `in-testing` the approval hook denies the call and prints a sentence
`Remove T0042 "<title>" (…): <reason>`: paste it as is in a chat message, wait for the user's OK, then repeat the
same call (approval in a subagent does not count). Use the plain command form only (no `bash -c`, `$(…)`,
variables, no word starting with `#`). `git revert` of the removal commit brings a todo back.

## Splitting a todo
- A todo too big for one atomic change is split (DESIGN.md 4.8), every part with its size, the other fields carried:
  `todo split ID --reason "..." [--blocks 1,2] --title T --size S [--acceptance A]... --title T2 --size S2 ...`.
- From `designed` a split that rewords, cuts, adds or drops an acceptance item, every split from `in-progress`, and
  every split with a part for another project (`--to-repo R --bucket B`) need the user's OK on the `Split …` sentence
  the approval hook prints, as for a removal.

## Test runs
A worker brief forbids measurement runs and full-suite runs unless the todo is a perf todo: it runs only the affected
tests (core rule "Tests stay quick", `.ai/shared/rules/core.md`); stress runs live in an opt-in script. A `tested` record without a run says so in
its `command` and `line` (e.g. `none` / `no code changed`).
Run a full suite, or any run likely to exceed a few minutes, as `todo run <ID> -- <command>` (DESIGN.md 19).
`todo show ID` may end with a `held:` block: those calls wait for the owner; continue independent work and name them
in the closing report.
The `test` block records a scripted run (`command`, `line`), a non-scripted end-to-end run (`test.kind=e2e`: an
agent or a person driving the app; `runner`, `steps`, `line`, no `command`) or a manual check with no command and no
app run (`test.kind=note`, e.g. a rules change read through: `steps`, `line`).

## Found work
A worker that finds new work outside its todo lists it in its closing report: title, why, proposed acceptance.
The orchestrator creates the todo (`draft`) from that report. Scope found inside a todo never widens that todo's
frozen acceptance; it becomes a new todo.

## Handovers
Handovers live at `handovers/<descr>_<YYYY-MM-DD>.md` in the main checkout. Create one with
`todo handover new <descr>`; the body is prose. `--todo ID` (repeatable) names the todos it is about and releases
the writing session's claims on them. The skeleton holds the front-matter (`date`, `author`, `todos`: every todo id
the note talks about, `branches`, `sessions`), the title `# Handover: …` and exactly these headings in order (an
empty one says `none`): `## Done`, `## Left, in order`, `## Open sessions and agents`, `## Next briefs`,
`## Key files`. A line under Done or Left, in order that starts with a todo id must name a live or done todo. `todo done` prunes a
note once every todo it names is done (DESIGN.md 7).

## Command reference
`todo <cmd> --help` prints the options and examples of every command (also `unblock attach fold bucket flow show
deliver stats audit priority serve`); one example each:

```
todo new --title "Parser keeps CRLF" --bucket core --priority P2 --size S --acceptance "parse() keeps CRLF" --acceptance "Test: tests/test_parse.py test_crlf"
```
```
todo set T0042 acceptance="parse() keeps CRLF" acceptance="Test: tests/test_parse.py test_crlf"
todo set T0042 --body-file body.md
```
```
todo move T0042 tested --evidence test.head=1a2b3c4 --evidence "test.command=python -I -X utf8 run_tests.py" --evidence "test.line=Ran 12 tests, OK"
```
```
todo block T0042 T0040
```
```
todo check
```
```
todo handover new parser-fix --todo T0042 --branch agent/t0042-crlf
```
```
todo commit
```
```
todo done --apply
```

## Without the tool
When the tool is not running, none of the process above applies (owner 2026-11-10): no states, roles, review
rounds or handover flow. Todos are plain files:
- **Pick work** from `todo/PRIORITY.md`, top down; read `todo/<ID>.md` for what to do.
- **Edit** `todo/<ID>.md` directly (UTF-8, LF), copying the shape of an existing one.
- **New todo:** next free id after the live `todo/` files; add its line to `PRIORITY.md`.
- **Done:** delete the file and drop its line from `PRIORITY.md`.
- Keep `PRIORITY.md` in step in the same commit, in the same line format, so the tool reads consistent data.
- When the tool is back, run `todo check` and `todo priority` and fix what they report.
