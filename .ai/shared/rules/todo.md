# ToDo and handovers (repos with `todo/_config.yaml`)

**ToDo** is the tool for open work. It replaced the repos' old to-do list files. It is **not** Claude Code's
Task/TaskCreate tools or any in-session task list: "todo" and "the ToDo tool" mean this tool.
One todo = one file `todo/<ID>.md`. `todo/PRIORITY.md` lists every open todo in the order to take it (priority,
then `order`, then id). Work from the repo's main checkout, never inside a worktree.

**The tool is available** when the `todo` command is on PATH (a shim for `todo.py`; run it from your own folder,
e.g. `todo list`). Then edit todos and handovers **only** through it (`todo list|show|new|set|move|…`); never by
hand or with plain git.
The tool rewrites `PRIORITY.md` in every todo commit. **CLI edits** (`new set move block unblock subtask`) are pending:
written to the working tree, not committed. Run `todo commit` when your CLI edits are done (every web or other
tool commit flushes them too). Every rule above the last section applies **only while the tool is running**;
**without the tool** agents use plain `todo/<ID>.md` files: see the last section.

**From another repo** (cwd in a different repo, or in none) only `todo new --repo <repo>` writes (reads run, other
writes are refused): a draft with `from_repo` and bucket `unset`, committed at once, or queued while the target
has a merge, rebase, cherry-pick or revert in progress and committed by its next write. `todo list --inbox` shows them.
The one write that starts in the home repo and writes another is `todo move-repo ID --to R --bucket B` (TD0147): it
recreates a `draft` to `designed` todo in R under R's next id, in its state (`from_repo`, `moved_from`), then removes
it here; DESIGN.md 15.

## States
`draft` → `refined` → `designed` → `in-progress` → `in-review` → `reviewed` → `in-testing` → `tested` → done.
Each move goes one step. A todo that needs no design (test below) goes to `designed` with `design: none` (stored
`design: [none]`); the web page shows it as a `no design` chip. `no-design-needed` is retired (TD0131): legacy files in it still validate and move on to `in-progress`, and any edit moves one to `designed` with `design: [none]` (owner 2026-11-12); nothing moves into it. Backward moves are allowed:
`in-review`/`reviewed`/`in-testing`/`tested` → `in-progress`, `reviewed`/`in-testing`/`tested` → `in-review`, and
`designed` → `refined`. A backward move clears `review` and `test`, and on redesign `design_review`.

| State | Means | Needs (cumulative) |
|---|---|---|
| `draft` | Someone wrote it down (a found-work report, the owner, a `TODO` note). A title (a short summary) is enough; other fields may be missing or rough. | — |
| `refined` | Refinement review done (below); acceptance still open to the design step | non-empty `acceptance` |
| `designed` | Design written and passed the design critics, or no design needed (`design: none`, test below); **acceptance frozen** | `design` link and `design_review` passed (any number of rounds); or `design: [none]`: never `size: L`, `size: S` or a move note with the reason |
| `in-progress` | Dispatched | `branch`; no live `blockers`; never `size: L` (split it: `todo.py split`) |
| `in-review` | Review lanes running | — |
| `reviewed` | Review lanes passed | `review` block: `head`, `lanes` |
| `in-testing` | Tests running on the reviewed head | — |
| `tested` | Tests passed on the reviewed head | `test` block: `head` equal to `review.head`, then `command`, `line` (scripted) or `kind: e2e`, `runner`, `steps`, `line` |

**Starting work: `todo.py assign ID`** (owner 2026-11-11). The first step of any work on a todo, in any state
(refine, design, implement), is `todo.py assign ID`: it claims the todo for this session and shows the cog. Refused:
another session holds it; do not work on it: stop and report (an orchestrator skips it and goes on). The message
names the `work clear` command for a finished holder; use it only on the owner's word or when that session is known
to be gone. "Needs --session" (no session id, e.g. opencode until TD0098): carry on without the claim. **When the
session's step is done** (the todo moved on: refined, designed, tested or done) **or it stops working on the todo**,
it releases the claim: `todo.py work clear ID --session <its session id, env CLAUDE_CODE_SESSION_ID>`, so the next
session (e.g. the IMPLEMENT launch after a refinement) can assign it. The release (and a handover's) folds the
todo's state commits made since the claim into one (TD0200, DESIGN.md 17).

**`draft` → `refined`: the refinement review.** The orchestrator refines the todo through the tool. It may
dispatch refiner agents (**at most 3 per wave**); they return proposed fields and **never write**, and the
orchestrator is the single writer during the wave. Refining means:
1. Set every field: `priority` from the project's goals, `bucket` from the files it touches (a draft from
   another repo arrives with `bucket: unset`, which must be replaced), `size`. Decide
   the **risk**: high when it touches a public API, persistence/schema, concurrency or another bucket, else low.
2. Write acceptance as checkable outcomes (an observable behaviour, an exact test or command, a file). Each one
   traces to the todo's problem, and nothing goes beyond it.
3. Check the claim against the code. Already implemented: say so with `file:line`. Duplicate or superseded:
   report it, don't refine it. Work wholly in another project -> `todo move-repo`; work in two projects ->
   `todo split ... --to-repo R` (the foreign part goes straight to R).
4. Make it **one atomic change** (one branch, one merge), even if the user-facing feature is still incomplete
   (a new data structure with no consumer yet is fine).
   - Too big: narrow it to the first slice and create the other slices as new `draft` todos, ordered with
     `blockers`.
   - Group by `bucket` and order by `blockers`. An umbrella parent with no acceptance of its own is unlinked and
     reported.
5. Put every uncertainty to the owner as a question. If one cannot wait, record it as a blocker or in the body;
   the todo stays `draft` until it is answered.

Then an **independent critic agent** (never the author) checks the todo. **Hard rule: review until green.** Apply
its findings, or reject them with a reason, then send the changed todo back to a critic for another round. Only a
round that returns PASS with no open **blocking** finding lets the todo move to `refined`; a written fix is not a
pass, and notes go to the design step. Rounds follow the core rule "Review loops converge": round 1 is a full pass
(each acceptance item checked for traceability, checkability against the code, and a named test), later rounds
verify the fixes only. Every critic brief states "round N of max 3" and, from round 2, carries the finding ledger and
the diff; the orchestrator never starts a round 4: after round 3 the open blocking findings go to the owner as
questions (reviews ran to round 9-14 on 2026-11-10). Round 1 also flags stress-style acceptance ("8 writers", many
iterations) and moves it to an opt-in script. A round counts only when it is complete: the critic ends with
`checked: …`, and anything it did not check (tool cap, error, no verdict) goes to a continuation critic for the rest
in the same round, not a new one. Round 2+ critics get a bigger word cap (about 40 tool calls, 500 words); a blocking
defect they find in unchanged text is reported as "missed in round 1". The move note names the rounds, every rejected finding and
`risk: high (<reasons>)` or `risk: low`, and ends with the **design verdict** (owner 2026-11-11): `design: needed
(<which point below holds>)` or `design: not needed (<why none holds>)`, by the no-design test
below. When not needed, the refining session sets `design=none` and moves the todo on to `designed` at once (that
move's note repeats the reason), so only todos that need a design reach the design step. The tool enforces only non-empty
acceptance; the critic is this rule's job.

**Lanes scale with the refinement's verdict** (trial, owner 2026-11-10). A todo of `size` M or L, or with
`risk: high`, gets two lanes in parallel in round 1 of every review: the refinement critic, the design critics and
the code review. In refinement and design the lanes are *code and scope* (every caller by grep, every cited anchor
resolved in the current tree) and *tests and edge cases* (a named test per acceptance item, failure paths, races); in
code review it never
takes the core map's one-lane or no-panel tier, and `risk: high` takes the core map's high-risk panel. An S todo with
`risk: low` gets one critic (refinement: decide size and risk before the critic runs). Round 2+ is one verifier on
the round-1 model with bigger caps (core rule). Move notes give rounds, lanes and the r2+ finding classes; after ~10
refinements compare with 2026-11-10 (mean ~4 rounds, S and M alike) and keep or drop the trial.

**`refined` → `designed` with `design: none`** (no design needed) right away when none of these holds:
- more than one plausible approach with a real trade-off;
- a new model, formula or derivation to work out;
- a change to a public API, persistence/schema or a cross-bucket interface;
- open owner questions.

Never for `size: L`. Otherwise the todo goes to design. The design step, too: work wholly in another project ->
`todo move-repo`; work in two projects -> `todo split ... --to-repo R`.

**A written design** is a Markdown file at `docs/design/<ID>-<slug>.md` (repo-relative), linked from the todo's
`design` field (e.g. `todo.py set <ID> design=docs/design/<ID>-<slug>.md` or `todo.py move <ID> designed --evidence
design=docs/design/<ID>-<slug>.md ...`). Create the file with the template in DESIGN.md §18 (TD0159): right after its
title comes `## Decisions in brief`, at most 2,000 characters, one line per owner decision and frozen choice, each
naming the section that explains it. The link target must exist at that repo-relative path; when set through the todo
tool, design-link existence is validated against the staged/index set of the write (not just the working tree), so
ensure the design document is present in the committed/staged state the tool validates (for example, commit or stage
the design file so it is included as the tool expects) before or as part of linking it. The design critics check that
`## Decisions in brief` is there, within 2,000 characters and in line with the rest of the doc. They also resolve
each anchor in its `## Anchors` against the current tree (the file exists and grep finds the symbol in it) and report
as blocking a line-number anchor or one that does not resolve.

**Acceptance freezes when the todo lands in `designed`** (owner 2026-11-10; also the legacy `no-design-needed`).
While it is `refined` the freeze is lifted: the design step, or the no-design check, puts every question that touches
acceptance to the owner, writes the answers into `acceptance`, and moves only when none is open. The design critics
check the changed acceptance along with the design. A backward move `designed` → `refined` lifts the freeze again,
and its questions go to the owner the same way.

Done is not a move target. `todo done --apply` fetches the remote done branch and deletes each `tested` todo that
a commit subject there names, when its `test.head` is on that branch and its own branch has not moved past it.
Without `--apply` it is a dry run. A parent without its own branch goes with its last
child. When the work landed in another repo, set `todo set ID work_repo=<repo>` (a `C:/dev`-relative name or an
absolute path) before `todo done`: it then checks `test.head` on that repo's done branch instead, with no subject
needed (TD0204, DESIGN.md 4.3). Old `tasks: …` commits and `tasks/` paths still count as history.

## Who moves them
The tools enforce the field and step rules above; the roles below are convention.
- **Orchestrator** (the agent that dispatches work): creates todos, moves them one step at a time, and sets `branch`
  at dispatch.
- **Implementer** (the agent working on the todo: the orchestrator itself or a session it dispatched) **delivers it
  to the end** (owner 2026-11-10), without waiting for anyone: records the review lanes' and test verdicts as
  evidence (`reviewed`, `tested`; the tests run after rebasing onto master), merges the branch to master with
  `--no-ff` at once (standing approval; no re-test on the merge, even if master moved: owner 2026-11-10), pushes
  (when there is a remote), runs `todo done --apply`, then removes its worktree and branch. It runs the affected
  tests on master after the push and fixes a failure forward. Its todo writes go to the main checkout
  (`--repo <main checkout>`), never to a worktree copy. Its worktree goes at `<main checkout>/.worktrees/<id>`,
  never a sibling folder: a path outside the session's folder makes every edit there ask the user (owner 2026-11-11).
  Before it changes course on a design point (keeps, revives, removes or reworks a part, interface or approach), it
  quotes the section of the project's design doc or the todo's design, or the dated owner decision, that settles it.
  The design decides intent; the code only shows the current state, so leftover code of a retired part is a thing
  to remove or report, not a decision to revive. When nothing settles it, it asks the owner before reworking
  (TD0125: retired MCP callers treated as live cost ~47 min).
  It, the review-lane briefs and the subagent briefs it writes read the design's `## Decisions in brief` and only
  the sections it names (TD0159); the full design doc is read only to review the design itself.
  Each phase is a persisted state (TD0145): the implementer moves to `in-review` when it starts the review lanes, to
  `reviewed` when they pass, to `in-testing` when it starts the tests and to `tested` when they pass, and runs
  `todo commit` after each move. A fix found in review stays in `in-review` and is re-reviewed on the new head; a
  fix found in testing moves back to `in-review`; a big change (new approach, changed design, a large diff; the
  implementer judges) moves back to `in-progress` with the reason in the move note.
  When a review or test needs the owner's input (TD0143), the implementer sets `owner_question=<one line>`, asks in
  chat, and keeps working on anything that does not depend on it; every forward move is refused while the field is
  set (humans too), and `todo done` keeps a waiting `tested` todo. Once answered it clears the field
  (`owner_question=`) and records the answer in the next move note or the body.
  When a todo waits on something outside the repo (TD0185, DESIGN.md 21), anyone sets
  `todo set ID external_blocker=<one line>`: forward moves, `todo assign` and web launches are refused, and
  `todo list`, PRIORITY.md and the web page hide it behind a counter (`todo list --external` shows them). Once the
  outside thing is resolved, lift it with `todo set ID external_blocker=` (its own commit, naming who lifted it).

  Neither changes `acceptance` from `designed` on, jumps over a state, or writes as human.
- **Review lanes, critics and the subagents an implementer spawns never write todos.** They supply verdicts, and
  the implementer records them.
- **Owner** (human): may change frozen acceptance, jump states, and mark a todo done from the web page (refused
  while it has live children). Human writes carry the trailer `Changed-By: human`.

## Flow rules
`todo.py assign ID` and `todo.py show ID` print the project's flow for the todo's bucket (`flow (<bucket|default>,
<phase>):`, TD0146; DESIGN.md 2.3). Read it at assign and follow it in each phase: `tests` is the kind of test run
that satisfies the test phase (`none`: record `none` in `test.command`/`line`), `owner_signoff: review` means the owner
looks at the output before the review passes (ask the owner), the `note` is the project's own instruction. Advisory:
the tool does not refuse a move for it (TD0151).

## Removing a todo
- A todo that should not exist (empty umbrella, duplicate, superseded) is removed with
  `todo.py remove ID --reason "..." [--repo R]`: one `todo: remove <ID>: <title>` commit with the reason.
- Refused: unknown id, live children (remove or re-parent them first), named as a live blocker (clear it first),
  and `tested` (it leaves through `todo done`).
- `draft` to `designed` (and the legacy `no-design-needed`): no approval. `in-progress`, `in-review`, `reviewed`,
  `in-testing`: the approval hook denies the call and
  prints a sentence `Remove T0042 "<title>" (state …, branch …): <reason>`; paste it as is in a chat message, wait
  for the user's OK, then repeat the same call. At most 10 sentences per OK; approval in a subagent does not count.
- Use the plain form only (no `bash -c`, `cmd /c`, `$(…)`, variables); raw `git rm`/`git commit` of an
  `in-progress`, `in-review`, `reviewed` or `in-testing` todo file is always denied (owner 2026-11-10). The gate is accident prevention
  for cooperating agents, not a security boundary. `git revert` of the removal commit brings a todo back.

## Splitting a todo
- A todo too big for one atomic change is split with
  `todo.py split ID --reason "..." [--blocks 1,2] --title T --size S [--acceptance A]... --title T2 --size S2 ...`:
  one `todo: split <ID> into <IDs>: <title>` commit (trailers `Split-From`, `Split-Into`, `Split-State`,
  `Split-Reason`) deletes it and adds 2+ todos in its state. Every part states its size; bucket, priority, parent,
  order, blockers, design and branch carry over; dependents are re-pointed in the same commit (`blocks`).
- Refused: fewer than 2 parts, live children, `in-review`, `reviewed`, `in-testing` and `tested`, `after` from
  `in-progress` on.
- `draft`, `refined`: no approval. `designed` (and the legacy `no-design-needed`): no approval when the split is verbatim (every
  acceptance item, unchanged, in exactly one part, nothing added); any reworded, cut, added or dropped item, and
  every split from `in-progress`, needs the user's OK on the `Split T0042 "…" …` sentence the approval hook prints,
  as for a removal (same plain-form rules; no word starting with `#`). Accident prevention, not a security boundary.
- A part for another project (TD0173): `todo split ... --title T2 --size S --to-repo R --bucket B` (B a bucket of R),
  only from `draft`, `refined` or `designed`, one R per split, at least one part stays home, no live blockers, no
  `--after`/`--blocks` naming it. R is written first (R's next id, `from_repo`, `split_from`; a design doc stays home and
  the part lands `refined`), then the split here. If the second write fails, the command prints a
  `todo.py remove <Rid> ... --repo R` to run from a folder in R. Every such split needs the user's OK on the sentence,
  in any state (it ends `foreign #2 to <R>`).

## Found work
A worker brief forbids measurement runs and full-suite runs unless the todo is a perf todo: it runs only the affected
tests (every test that could exercise a changed file; the full suite only when the change touches what every test
runs through; none when it touches no code: global "Tests stay quick"); stress runs live in an opt-in script. A `tested` record without a run says so in its
`command` and `line` (e.g. `none` / `no code changed`).
When an implementer runs its full suite, or any run likely to exceed a few minutes, it runs it as
`todo run <ID> -- <command>` (`--quiet-min N`, default 15): with the
watchdog installed a stall shows red in its tray and escalates once; without it the run is plain and prints and
exits the same (TD0127, DESIGN.md 19).
`todo show ID` ends with a `held:` block listing the watchdog's held calls (permission prompts and questions put on
hold while the owner was away, state `needs-user`) of the session assigned to ID in `todo/.working.json`; without
the watchdog, or with none held, the output is unchanged (TD0150, DESIGN.md 19). A held call waits for the owner:
continue independent work and name it in the closing report.
A non-scripted end-to-end run (an agent or a person driving the app, e.g. through a browser extension) is enough
for `tested` (TD0144): record it as `test.kind=e2e` with `test.runner` (who drove it, e.g. `claude via Claude in
Chrome`), `test.steps` (what was tried; commas are kept) and `test.line` (the observed result), and no `command`.
A worker that finds new work outside its todo lists it in its closing report: title, why, proposed acceptance.
The orchestrator creates the todo (`draft`) from that report. Scope found inside a todo never widens that todo's
frozen acceptance; it becomes a new todo.

## Handovers
Handovers live at `handovers/<descr>_<YYYY-MM-DD>.md` in the main checkout. Create one with
`todo handover new <descr>`; the body is prose. `--todo ID` (repeatable) names the todos it is about, and the
writing session's claims on them are released once the note is written (TD0140; session from `--session`, else
`CLAUDE_CODE_SESSION_ID`), so the next session's `todo.py assign` succeeds; a claim also lapses after 1 h
without a Bash/PowerShell call.

Front-matter keys, all present:
- `date`
- `author` (`agent`|`human`)
- `todos`: by convention every todo id the note talks about, each live or done (old notes say `tasks`)
- `branches`
- `sessions` (list of mappings)

The title is `# Handover: …`, then exactly these headings in order. An empty one says `none`:

    ## Done
    ## Left, in order
    ## Open sessions and agents
    ## Next briefs
    ## Key files

A line under Done or Left, in order that starts with a todo id must name a live or done todo. Lines without an id
(wrapped text, plain notes) are allowed.

**Lifecycle** (owner 2026-11-11): a handover note is deleted once every todo its `todos` names is done; git history
keeps it. In a git repo `todo done` lists them (`prune handovers/<file>`; a note with an empty `todos` list
once its `date` is more than 7 days old) and `--apply` deletes them in a `todo: handover prune` commit after the
done commit; without git, in a second write after the done write.

## Command reference
`todo <cmd> --help` prints these too. `new`: `--acceptance` repeats, one per criterion; `--repo R` from another repo.

```
todo new --title "Parser keeps CRLF" --bucket core --priority P2 --size S --acceptance "parse() keeps CRLF" --acceptance "Test: tests/test_parse.py test_crlf"
todo new --repo C:/dev/other --title "Found while working here"
```

`set`: `acceptance=` repeats and the given items replace the whole list (frozen from `designed` on); `--body-file F`
replaces the body with a UTF-8 file's text (`-` reads stdin); an empty value clears an optional field (not title,
priority, bucket, size).

```
todo set T0042 priority=P1 size=M also=cli,web
todo set T0042 acceptance="parse() keeps CRLF" acceptance="Test: tests/test_parse.py test_crlf"
todo set T0042 --body-file body.md
todo set T0042 owner_question=
```

`move`: one step at a time; `--evidence` repeats.

```
todo move T0042 in-progress --evidence branch=agent/t0042-crlf
todo move T0042 reviewed --evidence review.head=1a2b3c4 --evidence review.lanes=correctness,tests --note "round 1 PASS"
todo move T0042 tested --evidence test.head=1a2b3c4 --evidence "test.command=python -I -X utf8 run_tests.py" --evidence "test.line=Ran 12 tests, OK"
```

`block`: T0042 waits for T0040.

```
todo block T0042 T0040
todo unblock T0042 T0040
```

`check`: the files, or the staged files as the pre-commit hook does.

```
todo check
todo check --staged
```

`handover new`: writes `handovers/parser-fix_<date>.md` and releases the claim on T0042 of `--session` (default: env
`CLAUDE_CODE_SESSION_ID`).

```
todo handover new parser-fix --todo T0042 --branch agent/t0042-crlf
```

`commit`: commits the pending CLI edits (`new set move block unblock subtask`).

```
todo commit
```

`done`: a dry run without `--apply`.

```
todo done
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
