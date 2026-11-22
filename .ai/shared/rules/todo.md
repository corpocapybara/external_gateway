# ToDo and handovers (repos with `todo/_config.yaml`)

**ToDo** is the tool for open work. It replaced the repos' old to-do list files. It is **not** Claude Code's
Task/TaskCreate tools or any in-session task list: "todo" and "the ToDo tool" mean this tool.
One todo = one file `todo/<ID>.md`. `todo/PRIORITY.md` lists every open todo in the order to take it (priority,
then `order`, then id). Work from the repo's main checkout, never inside a worktree.

**The tool is available** when the `todo` command is on PATH (a shim that runs
`python -I -X utf8 C:/dev/shared/todo/todo.py`; run it from your own folder, e.g. `todo list`). Then edit todos and
handovers **only** through it (`todo list|show|new|set|move|…`); never by hand or with plain git. `todo <cmd> --help`
prints each command's options and examples; **DESIGN.md**, cited below by section, is the tool's design
(`C:/dev/shared/todo/DESIGN.md`): what the tool checks, refuses and prints is there, not here.
**CLI edits** (`set move block unblock`) are pending: written to the working tree, not committed; `new` and
`subtask` commit at once. Master gets a `todo:` commit only on creation, on a **park** (the claim released by
`todo work clear`, a handover or the session's end, or `owner_question` set or cleared, `external_blocker` set)
when the todo's content changed, on an `external_blocker` lift, and on done; workflow state (state, history,
review, test, branch) stays out of git (DESIGN.md 4.0, 14.7). `todo commit` commits every pending edit, workflow
included, when another clone must see it. Every rule above the last section applies **only while the tool is
running**; **without the tool** see the last section.

**From another repo** only `todo new --repo <repo>` writes (a draft; `todo list --inbox` there shows it; DESIGN.md 15).
Into a **non-git target** it is one write at once (id from `todo/_counter`, no commit, no queue); `todo split
--to-repo` (run from the home repo) writes a non-git target's parts as one nogit write (DESIGN.md 15, 16; design
`C:/dev/shared/todo/docs/design/nogit-cli-writes.md`). **Read another store** with `todo show ID --repo <repo>` /
`todo list --repo <repo>`, never by opening its `todo/*.md` or `_config.yaml`.

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
**A to-the-end run** (the owner or the brief says "to the end", deliver or finish it; owner 2026-11-21) assigns with
`todo assign ID --to-end`: the claim then survives step ends while the session works or waits on background work
(no re-assign after a move); it ends when a turn ends with nothing pending, on an API error (e.g. a usage limit), or
with the session: a desktop session once archived or deleted, a terminal session when its process exits.
`todo assign ID --worktree` also checks out the todo's branch (default `agent/<id>`) in `.worktrees/<ID>` and records
it in the `branch` field (TD0360).

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
verify the fixes only. Every critic brief states "round N" and, from round 2, carries the finding ledger
and the diff (bigger cap: about 40 tool calls, 500 words; a blocking defect in unchanged text is "missed in round
1"). There is no round limit: until a round is green, fix and run the next round. A round counts only
when the critic ends with `checked: …`; what it did not check goes to a continuation critic in the same round.
Record the move as `todo move ID refined --evidence refine_review.result=passed --evidence refine_review.rounds=N
--evidence refine_review.lanes=scope,tests --note "..."` (refused without it). The note names the rounds, every rejected finding and `risk: high (<reasons>)`
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
**Lane names**: `review.lanes` accepts only the names in the store's `review_lanes` (`todo/_config.yaml`; plus
`none` alone); any other value (e.g. a bracketed `[scope]`) is refused, in the moves below as `lane-names`. The high-risk panel's adversarial challenge is the lane `adversarial`
(owner 2026-11-19): record it in `review.lanes`; a store whose `review_lanes` lacks it adds it there first.
**Recorded and checked** (TD0280, owner 2026-11-21): the critic lanes go in `refine_review.lanes` and
`design_review.lanes` (*code and scope* as `scope`, *tests and edge cases* as `tests`; `none` is not a lane there).
An agent's forward move into `refined` (note names the risk and the design verdict), `designed` (design not none) or
`reviewed` is refused when its lanes do not fit size and risk as above; the refusal names the rule. A justified
exception: rerun with `--override "<reason>"` after ID STATE; every miss is logged for the daily review.
**Spawn check** (TD0437, owner 2026-11-21): the same moves also need, in the claiming session's Claude or opencode
transcript, a spawned agent naming the todo id per claimed lane (reviewed: agent type `review-<lane>` or the lane in
its description; refined/designed: as many refine/design spawns as the lane need above, 1 for an S todo with
risk low; TD0451), else `spawn-review` / `spawn-refine` /
`spawn-design`; a transcript that cannot be read is `spawn-unverified`. Both refuse like the checks above.
`TODO_SPAWN_CHECK=off` is for tests only: it skips the check and logs the skip.

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
  `todo done --apply`, then removes its worktree and branch; `todo deliver ID` does all of these from `tested`
  (from `reviewed` with `--test CMD`). It runs detached by default (TD0223): the run takes over the session's claim
  as a testing marker, so the implementer starts it, does not run `todo work clear`, and ends its turn (`--wait`
  runs it in the foreground). A failed run leaves a `failed` marker (and an owner_question when a step failed):
  the next session reads the run log the marker names, runs `todo work clear ID --session <S>`, fixes, then
  assigns. **Worktree step fails on Windows** (`git worktree remove`: `Permission denied` or `Device or resource
  busy`; a process holds a file): run `git worktree prune`. No longer listed (`git worktree list`): clear
  `owner_question` and the marker as above and rerun `todo deliver ID`; it skips the worktree, deletes the branch
  and runs `todo done`; delete the leftover folder later, once nothing holds it. Still listed: close what holds its
  files, run `git worktree remove --force <path>` (a plain remove refuses once files are partly deleted), then
  rerun. It
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
  `in-testing` when the tests start, `tested` when they pass; each move is kept in the main checkout's working tree
  and needs no commit (TD0338). A review fix stays
  in `in-review` (re-reviewed on the new head); a test fix moves back to `in-review`; a big change (new approach,
  changed design, a large diff) moves back to `in-progress` with the reason in the move note.
  When a review or test needs the owner's input, set `owner_question=<one line>`, ask in chat and keep working on
  what does not depend on it; once answered, clear it (`owner_question=`) and record the answer in the next move note.
  A sign-off (Flow rules) is the exception: ask in chat without setting it and clear one already set before asking,
  since an update after the OK makes that OK stale; the sign-off move clears one still set (TD0421).
  With the watchdog installed, `todo set`, `todo move` and a failed `todo deliver` step also send the question to
  it as a needs-user item (`approvals add`, DESIGN.md 19); a cleared or changed question removes the todo's stale
  needs-user items (`approvals remove`). An answer the owner gives there is recorded by the next `todo` command in
  this repo, a read such as `todo list` too (owner_question cleared, a history note `owner answered (watchdog
  <id>): <text>`). Not installed: nothing changes. The owner's web edits are not sent.
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
`owner_signoff: review` means the owner looks at the output before the review passes (ask the owner): the move
`in-review` -> `reviewed` by an agent is refused (`owner sign-off required: ask the owner in chat, then move with
--evidence review.signoff=owner`; enforced, TD0172). Ask the owner in chat, then run the move with `--evidence
review.signoff=owner` and the OK quoted in `--note` (the owner's own path stays `--by human`). The approval guard
denies that call first and prints the sentence `Sign off T0042 "<title>" (state in-review, branch <branch|none>):
reviewed`: paste it as is, wait for the owner's OK, rerun the call unchanged (TD0396; plain form only); touch the todo in between and the OK is stale (the
sign-off move clears owner_question itself, TD0421). Paraphrase the
OK in `--note` with no `$`, backticks, `%x%`, word starting `#` or doubled quotes (the guard denies those). The `note`
of the flow entry is the project's own instruction.

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

## Grooming
Grooming re-orders and merges the open todos of one repo, on the owner's request (owner 2026-11-20). It changes
fields, bodies and blockers and removes merged or obsolete todos through the CLI; it never moves a state. The rules
are the same in every repo; only the priority levels come from that repo's `todo/_config.yaml`.
1. Read every open todo (front-matter and body) and `todo/.working.json`. A todo a live session holds is not changed,
   even where rule 4 would move it; the plan names it. Check each premise against the current code, config and data
   (one bounded command each): a todo other work already did or made moot is removed with
   `--reason "obsolete: <evidence>"`; a blocker that is done is dropped (`todo unblock`); a stale path or name in a
   draft's acceptance is corrected; a todo whose acceptance looks met is named for its own flow to close.
2. Merge todos that change the same function or module and its tests, or fix the same proven root cause, when all
   are `draft`, none is held, the owner did not split them on purpose (a dated owner note), and the result is still
   one atomic change of size M or less. The survivor is the lowest id: it takes every acceptance item (deduplicated),
   each other body under a `Merged from <ID> (grooming <date>):` line, the combined size and the union of blockers;
   its priority is then set by rule 4. The others are removed with `--reason "merged into <survivor>"`.
3. Do not merge a size-S todo with checkable acceptance into a bigger one or one without acceptance, nor a todo with a
   proven cause into one whose cause is only suspected: order them with `todo block`, the quick or proven one first.
   Same area but separate code: leave both; block only when one builds on the other.
4. Priority: the levels of the repo's `priorities`; a repo without `P!` has no owner pin, one without `P4` puts a
   placeholder at its lowest level. `P!` only by the owner (at most 5). `P0`: master is broken now (a failing test, an unusable command).
   `P1`: a flaky test on master, lost edits or wrongly closed todos, a blocker of P1 work, or an owner request. `P2`: a
   defect with a workaround, or a measured cost. `P3`: rare, cosmetic, or an unproven premise. `P4`: a placeholder
   with nothing to act on. A blocked todo does not rank above its open blockers: lower it or raise the blocker. A
   priority the owner set (dated note) stays unless the owner agrees.
5. Inside one priority, `order` (`todo set ID order=N`; absent sorts last) puts an open blocker before what it
   blocks. A todo whose files another bucket owns moves to that bucket.
6. The plan lists every todo that waits on an owner answer (a question in its body or `owner_question`) with the
   question; they are asked together with the plan, and each answer goes into the todo body as a dated owner note.
   Grooming does not set `owner_question` (it stops forward moves).
7. Show the owner the plan (each merge, removal, block, priority, order and bucket change with its reason), apply it
   only on their OK, then `todo commit`. Have a critic try to break the plan first (core rule "Never present an unreviewed outcome").

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
UI work (a change a user sees in an app's UI; core rule "Tests stay quick") moves to `tested` only on a run in the
running app: `test.kind=e2e`, its `runner` and `steps` name who ran the run-app check and what was checked. If
the app could not be run, the todo does not move to `tested`: set `owner_question` naming the open check and ask
the owner.

## Found work
A small finding is fixed in the todo's own branch, never a new todo (core rule "Small findings are fixed along the
way", owner 2026-11-21): the worker fixes it with its test, and names it under "Fixed along the way" in the commit
and closing report. The frozen acceptance stays as it is; the fix rides along and the review lanes cover it.
A small finding that needs one or two owner answers is still fixed there: ask, keep working, fix it once answered.
Only bigger found work (or one that sits in files another session is editing) goes into the closing report: title,
why, proposed acceptance. The orchestrator creates one todo (`draft`) for the lot from that report. Such scope
never widens the todo's frozen acceptance.

## Handovers
Handovers live at `handovers/<descr>_<YYYY-MM-DD>.md` in the main checkout. Create one with
`todo handover new <descr>`; the body is prose. `--todo ID` (repeatable) names the todos it is about and releases
the writing session's claims on them. The skeleton holds the front-matter (`date`, `author`, `todos`: every todo id
the note talks about, `branches`, `sessions`), the title `# Handover: …` and exactly these headings in order (an
empty one says `none`; the skeleton writes it under all but `## Open sessions and agents`): `## Done`,
`## Left, in order`, `## Open sessions and agents`, `## Next briefs`, `## Key files`. `## Open sessions and agents`
lists each stopped or in-flight session and agent with its id and its output path, or the sha of the commit holding
its diff, in a `--body-file` note too; `none` only when none is stopped or in flight (the skeleton asks for this,
TD0321). A line under Done or Left, in order that starts with a todo id must name a live or done todo. `todo done`
prunes a note once every todo it names is done (DESIGN.md 7).

## Command reference
`todo <cmd> --help` prints the options and examples of every command (also `unblock attach fold bucket flow show
deliver stats audit priority serve`); one example each:

```
todo new --title "Parser keeps CRLF" --bucket core --priority P2 --size S --acceptance "parse() keeps CRLF" --acceptance "Test: tests/test_parse.py test_crlf"
```
```
todo set T0042 priority=P2 bucket=core size=S
todo set T0042 acceptance="parse() keeps CRLF" acceptance="Test: tests/test_parse.py test_crlf"
todo set T0042 --body-file - <<'EOF'
First line of the body.
Any number of lines, read from stdin.
EOF
```
```
todo move T0042 tested --evidence test.head=1a2b3c4 --evidence "test.command=python -I -X utf8 run_tests.py" --evidence "test.line=Ran 12 tests, OK"
todo move T0042 in-review --commit
```
```
todo block T0042 T0040
```
```
todo check
```
```
todo assign T0042
todo assign T0042 --worktree
todo rules in-testing
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
