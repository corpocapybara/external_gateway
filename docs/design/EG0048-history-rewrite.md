# EG0048 design: Rewrite git history to purge every leakcheck term, then force-push

## Decisions in brief
- D1: all rewrite material lives in one folder outside the repo, `C:/dev/_backup/egw-EG0048/` (mirror, commit-map, rules, ALLOW file, checker, rewritten clone); nothing in it is ever tracked (§Decisions D1).
- D2: one Python module `rules.py` there is the only replacement logic: a term-to-placeholder map, one case-insensitive word-bounded alternation, longest term first, case-preserving placeholders, ALLOW lines returned unchanged; the rewrite and the checker both import it (§Decisions D2).
- D3: the rewrite is a `git_filter_repo` library driver `rewrite.py` passing `rules.py` as blob, message (commit and tag) and filename callbacks, run once in a fresh clone of the mirror; this replaces the `--replace-text` / `--replace-message` flags of the refined acceptance (§Decisions D3).
- D4: the ALLOW file holds the exact email carbon-copy field lines, built from every history line holding the two-letter term and classified by hand; mixed lines are not in it and are listed for review (§Decisions D4).
- D5: the checker pairs `git diff -U0 -M OLD NEW` lines by position (equal counts per hunk, since no rule adds or removes a newline), and compares each commit pair of the commit-map: message and path set through `rules.py` (§Decisions D5).
- D6: run order and freeze window: todo to in-review, push, start record, mirror, rewrite, checks, owner sign-off, lease push, main checkout `reset --keep`; any failed precondition restarts from the start record (§Decisions D6).
- D7: failure handling: a rejected lease or a failed check before the push changes nothing; after the push only the owner orders the restore (§Decisions D7).
- D8: todo flow: `branch=master` (no extra ref may exist); review and test heads are rewritten SHAs recorded after the push; done via `todo done --apply` (§Decisions D8).
- D9: installing git-filter-repo is `python -m pip install --user git-filter-repo==2.47.0`, asked of the owner first (a download) (§Decisions D9).

## Acceptance
Restated from todo/EG0048.md, with the design step's changes (marked *changed*):
1. Start record (URL, worktrees, refs, stash, status, OLD = origin master, todo states) pasted with its UTC time; freeze until the main checkout moves; restart on any move.
2. Backup: filter-repo version; mirror of the main checkout outside the repo; fsck, rev-parse OLD, dry-run master push to URL; `gh api` push permission true, master not protected, *changed:* rulesets list empty; restore command (master only) on the owner's word.
3. *changed:* Rewrite by one `rewrite.py` run (filter-repo library, blob, message and filename callbacks from `rules.py`) in a fresh clone of the mirror; rules case-insensitive, word-bounded, longest first, covering every term the mirror scan reports plus EG0049's search value; ALLOW lines unchanged; commit-map kept.
4. Scan: `leakcheck history -root <main checkout> -allow ALLOW`: hits for every replaced term on the mirror, exit 0 on the rewritten clone.
5. Tree and allow-list checks: (a) removed lines have terms, added lines none; (b) checker: 0 mismatched line pairs, 0 changed ALLOW lines, *changed:* 0 mismatched commit messages and path sets over the commit-map; (c) *changed:* every ALLOW line found in a mirror blob is found in a clone blob (set, not count); (d) go build and go test of changed packages and importers.
6. Push with `--force-with-lease=master:OLD` to URL after ls-remote still shows OLD; ls-remote shows NEW, no other branch; fresh clone scan exit 0.
7. Main checkout moved with `git reset --keep`; status as in the start record; link.py all links ok; todo check; scan exit 0; test.head ancestor of NEW; cited SHAs left, commit-map translates.
8. Owner files the GitHub Support purge request; its id is added to the body when given.

## Anchors
- `cmd/tools/leakcheck/leakcheck.go`: `loadTerms`, `matchTerms`, `isWord` (term set and word-boundary semantics the rules mirror).
- `cmd/tools/leakcheck/main.go`: `run` (the `history` subcommand lands there with EG0058).
- `internal/connectors/email/connector.go`: `formatAddresses` (the carbon-copy field line kept by ALLOW).
- `internal/server/email_shape_test.go`: `leakcheck:allow` (field lines already marked).
- `config.example/operations.yaml`: `allow_fields` (email op field list holding the field name).
- `todo/_config.yaml`: `deploy` (flow: scripted tests, owner sign-off on review).
- `.ai/shared/rules/todo.md`: `Who moves them` (delivery steps D8 adapts).

## Decisions

### D1 Working folder
`C:/dev/_backup/egw-EG0048/` holds `mirror.git` (`git clone --mirror` of the main checkout), `commit-map` (copied from the rewritten clone's `.git/filter-repo/commit-map`), `map.txt` (term ==> placeholder, one per line), `rules.py`, `rewrite.py`, `check.py`, `ALLOW.txt`, `work/` (the rewritten clone) and `log.md` (commands and outputs, pasted into the todo body without terms). It is outside every repo and outside the scratchpad (which a reboot may clear). The real terms appear only there and in the gitignored `.ai/` files.

### D2 Rules module
`map.txt` maps each term `leakcheck history` reports on the mirror, plus EG0049's search value, to a placeholder: the LOCAL.md placeholders for the workspaces, Jira keys, company names and tunnel (section "Real names in tracked files"), `<name>.example.com` hosts for hosts (EG0049 used `jira-server.example.com`), `jdoe` for the first name. `rules.py` loads it, refuses to start when a term has no placeholder, and builds one Python `re` pattern over bytes: `(?i)(?<![A-Za-z0-9_])(?:t1|t2|...)(?![A-Za-z0-9_])`, terms sorted longest first, the boundary taken only at a word-character term edge (as leakcheck `contains`). A single alternation scans each position once, so a placeholder is never rescanned and the Jira key wins over the workspace name it contains. The replacement keeps the case class of the match: all upper gives the placeholder upper-cased, a capital first letter capitalises it, else as written. `apply(data: bytes) -> bytes` splits on `\n`, keeps a line whose `strip()` is in ALLOW unchanged, and substitutes the rest; `\r` stays with its line. No rule contains a newline, so line counts never change.

### D3 Rewrite driver
`rewrite.py` imports `git_filter_repo` and `rules`, and runs `RepoFilter(FilteringOptions.parse_args([]), blob_callback=..., message_callback=..., filename_callback=...).run()` (no options: the defaults, no `--force`) in `work/`, a fresh `git clone --no-local mirror.git work`, so filter-repo's fresh-clone check passes without `--force`. `blob_callback` sets `blob.data = rules.apply(blob.data)`; `message_callback` returns `rules.apply(message)` (filter-repo calls it for commit and tag messages); `filename_callback` returns `rules.apply(filename)`. Identities and ref names have no hit (checked 2026-11-16) and get no callback; the scan proves it again. Compared with `--replace-text`: a callback sees whole lines, so the ALLOW exemption is exact, and the checker reuses the same code instead of a second regex dialect.

### D4 ALLOW file
A one-off Python scan over the mirror lists every distinct stripped line, in any blob or message of history, that holds the two-letter term as a word. Each is classified by hand: field (the carbon-copy field in code, config, test or a sentence about the field) or workspace. `ALLOW.txt` holds the field lines exactly. A mixed line (both uses) is left out, so its field mention is replaced too; such lines are prose in todos or notes, and their list goes into the review brief. The body records the file's path, line count and the count of mixed lines.

### D5 Checker
`check.py OLD NEW` in `work/` prints three counts, each must be 0:
- line pairs: `git diff -U0 -M --no-color OLD NEW`; per hunk the removed and added counts must be equal (else a mismatch); each removed line through `rules.apply` must equal its added line; renamed paths must equal `rules.apply(old path)`.
- ALLOW: `rules.apply(line) != line` for any ALLOW line.
- commit pairs: for each `old new` line of the commit-map (skipping the header and deleted commits), `rules.apply(old message) == new message` and `{rules.apply(p) for p in ls-tree(old)} == set(ls-tree(new))`.
It also prints the ALLOW membership check of acceptance 5(c): ALLOW lines present in a mirror blob but in no clone blob (must be 0).

### D6 Run order
1. Prerequisites: EG0030, EG0049, EG0058 done; every other worktree and branch gone; terms file gains EG0049's search value; filter-repo installed (D9).
2. `todo move EG0048 in-progress`, then `in-review`, `todo commit`, `git push origin master`: the in-review state is part of OLD.
3. Start record (acceptance 1) into `log.md`; from here no commit in the main checkout and no pending todo write is committed.
4. Mirror, fsck, dry-run, gh queries (acceptance 2). Build `map.txt`, `ALLOW.txt` (D4).
5. Mirror scan: `leakcheck history -root C:/dev/apps/external_gateway -allow ALLOW.txt` in `mirror.git`; every hit term is in `map.txt`.
6. `work/` clone, `rewrite.py`, copy commit-map, `check.py`, scan of `work/`, go build and go test (acceptance 3-5).
7. Review lanes on the evidence (D8); owner sign-off on the review output (deploy flow).
8. `git -C work remote add origin URL`; `git ls-remote URL refs/heads/master` = OLD; `git -C work push --force-with-lease=master:OLD origin master`; ls-remote; fresh clone scan (acceptance 6).
9. Main checkout: `git fetch origin`, `git reset --keep origin/master`, status, link.py, todo check, scan (acceptance 7).
10. Record the todo moves (D8).

### D7 Failures
- Any check in steps 4-7 fails: fix `map.txt`, `ALLOW.txt` or `rules.py`, delete `work/`, redo step 6; origin is untouched.
- Master or origin moved after step 3: restart at step 2.
- Lease rejected at step 8: origin moved; nothing was written; restart at step 2.
- A single-ref push either lands or not: ls-remote shows OLD (retry step 8) or NEW (continue).
- A check after the push fails: stop and ask the owner; restore (`git -C mirror.git push --force URL OLD:refs/heads/master`) only on the owner's word.

### D8 Todo flow
The start condition forbids any branch but master, so `branch=master` (no `EG0048` ref). Review lanes (deploy risk high: the core map's high-risk panel) read this section list, `check.py` output, the scan outputs, `rules.py`, the counts of `ALLOW.txt` and its mixed-line list, and the diff `OLD..NEW` in `work/`; they never see `map.txt` terms in their output. After the push: `todo move EG0048 reviewed --evidence review.head=NEW`, `in-testing`, `tested` with `test.head=NEW`, `test.command` = the scan command on the fresh clone, `test.line` its count line, then `todo commit`, `git push`, `todo done --apply` (master holds `todo: EG0048 …` subjects and NEW). If the tool refuses `branch=master`, the move note records it and a ref-less name is used.

### D9 Installing git-filter-repo
`python -m pip install --user git-filter-repo==2.47.0` (PyPI, checked available 2026-11-16) after the owner's OK, since it is a download. `git filter-repo --version` and `python -I -X utf8 -c "import git_filter_repo"` prove it.

## Test plan
1. Start record: the commands of acceptance 1 pasted with their output and time (test: `log.md` section "start").
2. Backup: `git -C mirror.git fsck`, `rev-parse master`, the dry-run push, three `gh api` queries (test: `log.md` section "backup").
3. Rewrite: `python rewrite.py` exit 0; commit-map copied; `rules.py` self-test (`python rules.py --selftest`: Jira key before workspace name, case classes, word edges, ALLOW line kept, missing placeholder refused).
4. Scan: `leakcheck history` on the mirror (hits listed per term) and on `work/` (exit 0, count line 0 hits): the recorded test.
5. Tree and allow-list: `python check.py OLD NEW` prints 0, 0, 0 and 0; `go build ./...`; `go test` of the changed packages and importers.
6. Push: ls-remote before and after; fresh clone scan exit 0.
7. Main checkout: `git status --porcelain` diff against the start record empty; `link.py`; `todo check`; scan exit 0; `git merge-base --is-ancestor`.
8. Purge decision: body line present.

## Not changed
- No tracked code changes in EG0048 (the scan tool is EG0058). Author and committer identities, ref names other than master, and other repos are untouched.
- SHAs cited in todos, handovers, design docs and commit messages stay as written; the commit-map translates them.
- The main checkout's old objects and reflogs stay local (owner-only `git reflog expire` and `git gc`).
- `refs/pull/*` on GitHub keep old SHAs until the owner's purge request.
