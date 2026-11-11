# ToDo and handovers (repos with `todo/_config.yaml`)

**ToDo** is the tool for open work. It replaced the repos' old to-do list files. It is **not** Claude Code's
Task/TaskCreate tools or any in-session task list: "todo" and "the ToDo tool" mean this tool.
One todo = one file `todo/<ID>.md`. `todo/PRIORITY.md` lists every open todo in the order to take it (priority,
then `order`, then id). Work from the repo's main checkout, never inside a worktree.

**The tool is available** when the `todo.py` CLI is installed. Then edit todos and handovers **only** through it
(`todo list|show|new|set|move|…`); never by hand or with plain git.
The tool rewrites `PRIORITY.md` in every todo commit. **CLI edits** (`new set move block subtask`) are pending:
written to the working tree, not committed. Run `todo commit` when your CLI edits are done (every web or other
tool commit flushes them too). Every rule above the last section applies **only while the tool is running**;
**without the tool** agents use plain `todo/<ID>.md` files: see the last section.

**From another repo** (cwd in a different repo, or in none) only `todo new --repo <repo>` writes (reads run, other
writes are refused): a draft with `from_repo` and bucket `unset`, committed at once, or queued while the target
has a merge, rebase, cherry-pick or revert in progress and committed by its next write. `todo list --inbox` and the web Inbox filter show them.

## States
`draft` → `refined` → `designed` or `no-design-needed` → `in-progress` → `code-reviewed` → `tested` → done. Each
move goes one step. Backward moves are allowed: `code-reviewed`/`tested` → `in-progress` and `designed` →
`refined`. A backward move clears `review` and `test`, and on redesign `design_review`.

| State | Means | Needs (cumulative) |
|---|---|---|
| `draft` | Someone wrote it down (a found-work report, the owner, a `TODO` note). A title (a short summary) is enough; other fields may be missing or rough. | — |
| `refined` | Refinement review done (below); acceptance still open to the design step | non-empty `acceptance` |
| `designed` | Design written and passed the design critics; **acceptance frozen** | `design` link; `design_review` passed (any number of rounds) |
| `no-design-needed` | No design needed (test below); **acceptance frozen** | never `size: L`; `size: S`, or a move note with the reason |
| `in-progress` | Dispatched | `branch`; no live `blockers`; never `size: L` (split it: `todo.py split`) |
| `code-reviewed` | Review lanes passed | `review` block: `head`, `lanes` |
| `tested` | Tests passed on the reviewed head | `test` block: `head` equal to `review.head`, `command`, `line` |

**`draft` → `refined`: the refinement review.** The orchestrator refines the todo through the tool. It may
dispatch refiner agents (**at most 3 per wave**); they return proposed fields and **never write**, and the
orchestrator is the single writer during the wave. Refining means:
1. Set every field: `priority` from the project's goals, `bucket` from the files it touches (a draft from
   another repo arrives with `bucket: unset`, which must be replaced), `size`. Decide
   the **risk**: high when it touches a public API, persistence/schema, concurrency or another bucket, else low.
2. Write acceptance as checkable outcomes (an observable behaviour, an exact test or command, a file). Each one
   traces to the todo's problem, and nothing goes beyond it.
3. Check the claim against the code. Already implemented: say so with `file:line`. Duplicate or superseded:
   report it, don't refine it.
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
`risk: high (<reasons>)` or `risk: low`. The tool enforces only non-empty acceptance; the critic is this rule's job.

**Lanes scale with the refinement's verdict** (trial, owner 2026-11-10). A todo of `size` M or L, or with
`risk: high`, gets two lanes in parallel in round 1 of every review: the refinement critic, the design critics and
the code review. In refinement and design the lanes are *code and scope* (every caller by grep, every cited line
re-read) and *tests and edge cases* (a named test per acceptance item, failure paths, races); in code review it never
takes the core map's one-lane or no-panel tier, and `risk: high` takes the core map's high-risk panel. An S todo with
`risk: low` gets one critic (refinement: decide size and risk before the critic runs). Round 2+ is one verifier on
the round-1 model with bigger caps (core rule). Move notes give rounds, lanes and the r2+ finding classes; after ~10
refinements compare with 2026-11-10 (mean ~4 rounds, S and M alike) and keep or drop the trial.

**`refined` → `no-design-needed`** right away when none of these holds:
- more than one plausible approach with a real trade-off;
- a new model, formula or derivation to work out;
- a change to a public API, persistence/schema or a cross-bucket interface;
- open owner questions.

Never for `size: L`. Otherwise the todo goes to design.

**Acceptance freezes when the todo lands in `designed` or `no-design-needed`** (owner 2026-11-10). While it is
`refined` the freeze is lifted: the design step, or the no-design-needed check, puts every question that touches
acceptance to the owner, writes the answers into `acceptance`, and moves only when none is open. The design critics
check the changed acceptance along with the design. A backward move `designed` → `refined` lifts the freeze again,
and its questions go to the owner the same way.

Done is not a move target. `todo done --apply` fetches the remote done branch and deletes each `tested` todo that
a commit subject there names, when its `test.head` is on that branch and its own branch has not moved past it.
Without `--apply` it is a dry run. A parent without its own branch goes with its last
child. Old `tasks: …` commits and `tasks/` paths still count as history.

## Who moves them
The tools enforce the field and step rules above; the roles below are convention.
- **Orchestrator** (the agent that dispatches work): creates todos, moves them one step at a time, and sets `branch`
  at dispatch.
- **Implementer** (the agent working on the todo: the orchestrator itself or a session it dispatched) **delivers it
  to the end** (owner 2026-11-10), without waiting for anyone: records the review lanes' and test verdicts as
  evidence (`code-reviewed`, `tested`; the tests run after rebasing onto master), merges the branch to master with
  `--no-ff` at once (standing approval; no re-test on the merge, even if master moved: owner 2026-11-10), pushes
  (when there is a remote), runs `todo done --apply`, then removes its worktree and branch. It runs the affected
  tests on master after the push and fixes a failure forward. Its todo writes go to the main checkout
  (`--repo <main checkout>`), never to a worktree copy.

  Neither changes `acceptance` from `designed` / `no-design-needed` on, jumps over a state, or writes as human.
- **Review lanes, critics and the subagents an implementer spawns never write todos.** They supply verdicts, and
  the implementer records them.
- **Owner** (human): may change frozen acceptance, jump states, and mark a todo done from the web page (refused
  while it has live children). Human writes carry the trailer `Changed-By: human`.

## Removing a todo
- A todo that should not exist (empty umbrella, duplicate, superseded) is removed with
  `todo.py remove ID --reason "..." [--repo R]`: one `todo: remove <ID>: <title>` commit with the reason.
- Refused: unknown id, live children (remove or re-parent them first), named as a live blocker (clear it first),
  and `tested` (it leaves through `todo done`).
- `draft` to `no-design-needed`: no approval. `in-progress`, `code-reviewed`: the approval hook denies the call and
  prints a sentence `Remove T0042 "<title>" (state …, branch …): <reason>`; paste it as is in a chat message, wait
  for the user's OK, then repeat the same call. At most 10 sentences per OK; approval in a subagent does not count.
- Use the plain form only (no `bash -c`, `cmd /c`, `$(…)`, variables); raw `git rm`/`git commit` of an
  `in-progress` or `code-reviewed` todo file is always denied (owner 2026-11-10). The gate is accident prevention
  for cooperating agents, not a security boundary. `git revert` of the removal commit brings a todo back.

## Splitting a todo
- A todo too big for one atomic change is split with
  `todo.py split ID --reason "..." [--blocks 1,2] --title T --size S [--acceptance A]... --title T2 --size S2 ...`:
  one `todo: split <ID> into <IDs>: <title>` commit (trailers `Split-From`, `Split-Into`, `Split-State`,
  `Split-Reason`) deletes it and adds 2+ todos in its state. Every part states its size; bucket, priority, parent,
  order, blockers, design and branch carry over; dependents are re-pointed in the same commit (`blocks`).
- Refused: fewer than 2 parts, live children, `code-reviewed` and `tested`, `after` from `in-progress` on.
- `draft`, `refined`: no approval. `designed`, `no-design-needed`: no approval when the split is verbatim (every
  acceptance item, unchanged, in exactly one part, nothing added); any reworded, cut, added or dropped item, and
  every split from `in-progress`, needs the user's OK on the `Split T0042 "…" …` sentence the approval hook prints,
  as for a removal (same plain-form rules; no word starting with `#`). Accident prevention, not a security boundary.

## Found work
A worker brief forbids measurement runs and full-suite runs unless the todo is a perf todo: it runs only the affected
tests (full suite once, before the merge); stress runs live in an opt-in script.
A worker that finds new work outside its todo lists it in its closing report: title, why, proposed acceptance.
The orchestrator creates the todo (`draft`) from that report. Scope found inside a todo never widens that todo's
frozen acceptance; it becomes a new todo.

## Handovers
Handovers live at `handovers/<descr>_<YYYY-MM-DD>.md` in the main checkout. Create one with
`todo handover new <descr>`; the body is prose.

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
keeps it. Until the automation (ToDo repo T0101) lands, notes are kept.

## Without the tool
When the tool is not running, none of the process above applies (owner 2026-11-10): no states, roles, review
rounds or handover flow. Todos are plain files:
- **Pick work** from `todo/PRIORITY.md`, top down; read `todo/<ID>.md` for what to do.
- **Edit** `todo/<ID>.md` directly (UTF-8, LF), copying the shape of an existing one.
- **New todo:** next free id after the live `todo/` files; add its line to `PRIORITY.md`.
- **Done:** delete the file and drop its line from `PRIORITY.md`.
- Keep `PRIORITY.md` in step in the same commit, in the same line format, so the tool reads consistent data.
- When the tool is back, run `todo check` and `todo priority` and fix what they report.
