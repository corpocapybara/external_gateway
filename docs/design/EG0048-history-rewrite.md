# EG0048 design: Rewrite git history to purge every leakcheck term, then force-push

## Decisions in brief
- D1: all rewrite material lives in one folder outside the repo, `C:/dev/_backup/egw-EG0048/` (mirror, commit-map, scripts, ALLOW file, rewritten clone); nothing in it is ever tracked, and nothing in it is deleted before EG0048 is done (§D1).
- D2: one Python module `rules.py` is the only replacement logic, and its matcher equals leakcheck's: UTF-8 decoded with surrogateescape, Go-compatible lower-casing and word runes, a boundary only at a word-rune term edge, longest term first, ALLOW lines and `leakcheck:allow` lines unchanged as in `leakcheck history`; it refuses an ALLOW line that holds any term but the field term (§D2).
- D3: the rewrite is a `git_filter_repo` library driver `rewrite.py` with blob, message and filename callbacks from `rules.py`, `prune_empty` and `prune_degenerate` set to `'never'`, and filter-repo's commit-SHA translation in messages left on (so `Done-Upto` trailers keep naming real commits); run once in a fresh clone of the mirror (§D3).
- D4: `prep.py` builds the ALLOW candidates and runs three pre-checks on the mirror before any rewrite: no two paths of one commit tree map to one new path (also case-insensitively), no `leakcheck:allow` line holds a term but the field term, every hit term has a placeholder (§D4).
- D5: `check.py` walks every commit-map pair: commit counts equal, no pruned commit, each old tree maps entry by entry to the new tree (path through the rules, blob id of the rewritten content, binary blobs unchanged), messages equal through the rules plus SHA translation; `git diff -U0` stays as an extra (§D5).
- D6: run order and freeze window, with `state.py snapshot|backup|remote|compare` recording each state; step 9 re-checks master = OLD and origin = NEW before `reset --keep` (§D6).
- D7: failure handling, including the half-done state (origin NEW, main checkout OLD) (§D7).
- D8: todo flow: `branch=master`, which `todo done` does not check (DESIGN.md §4.3, TD0332); merged signal from the existing `EG0048:` subjects; rehearsed on a scratch clone of the rewritten repo before the push (§D8).
- D9: git-filter-repo 2.47.0 installed with pip --user; drivers run as `python -X utf8` (§D9).

## Acceptance
Restated from todo/EG0048.md (the design step's changes are marked *changed*; round 2 changed items 3, 5, 6 and 7):
1. Start record (URL, worktrees, refs, stash, status, OLD = origin master, todo states) pasted with its UTC time; freeze until the main checkout moves; restart on any move.
2. Backup: filter-repo version; mirror of the main checkout outside the repo; fsck, rev-parse OLD, dry-run master push to URL; `gh api` push permission true, master not protected, *changed:* rulesets list empty; restore command (master only) on the owner's word.
3. *changed:* Rewrite by one `rewrite.py` run (filter-repo library; blob, message and filename callbacks from `rules.py`; `prune_empty` and `prune_degenerate` `'never'`; SHA translation in messages on) in a fresh clone of the mirror, after `prep.py` reports 0 path collisions and 0 marker lines with a non-field term; the matcher equals leakcheck's; ALLOW lines and `leakcheck:allow` lines unchanged; an ALLOW line with a non-field term is refused; commit-map kept.
4. Scan: `leakcheck history -root <main checkout> -allow ALLOW`: hits for every replaced term on the mirror, exit 0 on the rewritten clone.
5. *changed:* Tree and allow-list checks: (a) removed lines have terms, added lines none; (b) `check.py` prints 0 for: commit-count difference, pruned commits, tree mismatches over the commit-map (entry by entry), message mismatches, changed ALLOW lines, an unresolvable newest `Done-Upto`; its `--selftest` fails a wrong rewrite of a fixture repo; (c) every ALLOW line found in a mirror blob is found in a clone blob; (d) go build and go test of changed packages and importers.
6. *changed:* Push with `--force-with-lease=master:OLD` to URL after ls-remote still shows OLD; ls-remote shows NEW, no other branch; fresh clone scan exit 0; the moves and `todo done` dry run were rehearsed on a scratch clone of the rewritten repo before the push.
7. *changed:* Main checkout: master still OLD and ls-remote NEW re-checked, then moved with `git reset --keep`; status as in the start record; link.py all links ok; todo check; scan exit 0; test.head ancestor of NEW; cited SHAs in files left, commit-map translates.
8. Owner files the GitHub Support purge request; its id is added to the body when given.

## Anchors
- `cmd/tools/leakcheck/leakcheck.go`: `loadTerms`, `isWord`, `contains`, `matchTerms`, `lineHits` (term set, lower-casing, word edges, marker rule the rules mirror).
- `cmd/tools/leakcheck/history.go`: `runHistory` ALLOW parsing (BOM, `TrimSpace`, blank and `#` lines) and `lineScan` (split on `\n`, `\r` trimmed); paths go through `matchTerms` with no ALLOW or marker exemption; blobs are scanned whole, binary too.
- `internal/connectors/email/connector.go`: `formatAddresses` (the carbon-copy field line kept by ALLOW).
- `internal/server/email_shape_test.go`: `leakcheck:allow` (field lines already marked).
- `config.example/operations.yaml`: `allow_fields` (email op field list holding the field name).
- `todo/_config.yaml`: `deploy` (flow: scripted tests, owner sign-off on review).
- `.ai/shared/rules/todo.md`: `Who moves them`, `todo done` paragraph (TD0332).
- `C:/dev/_tools/todo/todofmt/done.py`: `_watermark`, `_log` (`Done-Upto` lookup and its merge-base).
- git-filter-repo 2.47.0 `git_filter_repo.py`: `_tweak_commit`, `_translate_commit_hash`, `_prunable`, `_maybe_trim_extra_parents`, blob handling before `blob_callback`, origin removal.

## Decisions

### D1 Working folder
`C:/dev/_backup/egw-EG0048/` holds `mirror.git` (`git clone --mirror` of the main checkout), `commit-map` and `suboptimal-issues` (copied from `work/.git/filter-repo/`), `map.txt`, `ALLOW.txt`, the scripts `state.py`, `prep.py`, `rules.py`, `rewrite.py`, `check.py`, the rewritten clone `work/`, the rehearsal clone `dry/` (D8), the fixture `fixture/` (D5) and `log.md` (commands and outputs, pasted into the todo body without terms). It is outside every repo and outside the scratchpad (which a reboot may clear). The real terms appear only there and in the gitignored `.ai/` files. Nothing in the folder is deleted before `todo done` removed EG0048; afterwards the owner decides (the mirror is the only full copy of OLD).

### D2 Rules module
`map.txt` is UTF-8: a first line `field: <term>` naming the field term (the two-letter workspace name that is also the email carbon-copy field name), then one `term ==> placeholder` line per term `leakcheck history` reports on the mirror plus EG0049's search value. Placeholders: the LOCAL.md placeholders for workspaces, Jira keys, company names and tunnel; `<name>.example.com` for hosts (EG0049 used `jira-server.example.com`); `jdoe` for the first name. `rules.py` refuses to start when a term has no placeholder, a placeholder matches any term, or a placeholder holds a run of 7 or more hex digits (it would meet filter-repo's SHA translation, D3).

Matcher, equal to leakcheck's (`contains` and `matchTerms` in leakcheck.go):
- Text: bytes are decoded as UTF-8 with `surrogateescape` and encoded back the same way, so unchanged bytes round-trip exactly; an invalid byte is a non-word neighbour (Go reads it as U+FFFD, category So, also non-word).
- Lower-casing: `golower(c)` per code point, `c.lower()` when that is one code point, U+0130 to `i` (Go's `unicode.ToLower`; it is the only code point whose Python lower-case is longer, which `rules.py` asserts at start-up). The lowered line has the same length as the original, so a span found in it is the same span in the original. Terms are lower-cased when loaded, as `loadTerms`.
- Word runes: `_`, Unicode category L*, or Nd (Go `isWord`; Python `\w` also counts No and Nl, so it is not used). `rules.py` builds a character class `W` of every such code point at start-up. Python 3.14 has Unicode 16.0 tables, Go 15.0; they differ only on code points new in 16.0, and `rules.py` prints its `unicodedata.unidata_version` into `log.md`.
- Pattern over the lowered line: one alternation of the escaped terms, longest first (ties by term), each term `t` wrapped as `(?<![W])t` when its first rune is a word rune and `t(?![W])` when its last one is, so the boundary is taken only at a word-rune edge and the neighbours are read in the lowered line, as `contains`. One left-to-right `finditer` replaces each match once; a placeholder is never rescanned; the Jira key wins over the workspace name it contains.
- Case class of the replacement: the original match all upper gives the placeholder upper-cased, a capital first letter capitalises it, else the placeholder as written.

Units (as `leakcheck history`):
- `apply_text(data)`, for blobs and commit or tag messages: split on `\n`; a line is kept unchanged when `key(line)` (the line without a trailing `\r`, stripped of Go's `unicode.IsSpace` set, which is Python's whitespace minus U+001C..U+001F) is in ALLOW, or when the line contains `leakcheck:allow` (as `lineHits`); other lines are substituted; `\r` stays with its line. No rule contains a newline, so line counts never change.
- `apply_path(name)`, for paths: substitution with no ALLOW or marker exemption (history.go scans paths with `matchTerms` alone).
- `ALLOW.txt` is parsed as history.go: BOM stripped, split on `\n`, each line stripped with the same set, blank and `#` lines skipped. `rules.py` refuses to start, naming the ALLOW line number, when any ALLOW line matches a term other than the field term.

`python -X utf8 rules.py --selftest` covers: Jira key before the workspace name; each case class; word edges next to `_`, a digit, a Polish letter, `İ`, a superscript digit (No, a boundary in Go); terms inside URLs, `egw/<x>/` and `config.<x>.yaml`; a multi-word term; idempotence (`apply(apply(x)) == apply(x)`); no placeholder matches a term; existing placeholders untouched; a hex SHA holding the field term inside it untouched; an ALLOW line kept byte for byte (with `\r`, leading spaces, BOM-prefixed ALLOW file); a `leakcheck:allow` line kept; an ALLOW line with a non-field term refused; a missing placeholder refused; invalid UTF-8 round-tripping. It runs on fake terms from its own fixture map, never `map.txt`.

### D3 Rewrite driver
`rewrite.py` imports `git_filter_repo` and `rules`, takes `args = FilteringOptions.default_options()` (that is `parse_args([], error_on_empty=False)`; plain `parse_args([])` exits "No arguments specified"), sets `args.prune_empty = 'never'` and `args.prune_degenerate = 'never'`, and runs `RepoFilter(args, blob_callback=..., message_callback=..., filename_callback=...).run()` in `work/`, a fresh `git clone --no-local mirror.git work`, so the fresh-clone check passes without `--force`.
- `blob_callback(blob, metadata)`: when `b"\0" not in blob.data[:8192]` (filter-repo's own binary test before `--replace-text`), `blob.data = rules.apply_text(blob.data)`; a binary blob is left as is. `leakcheck history` scans binary blobs too, so a binary blob with a term fails the scan (acceptance 4) and is handled by hand then.
- `message_callback(message)`: `rules.apply_text(message)`; filter-repo calls it for commit and tag messages (no tags exist).
- `filename_callback(filename)`: `rules.apply_path(filename)`.
- Pruning: with the defaults (`'auto'`), a commit that became empty would be dropped (e.g. a commit that only replaced a real name by its placeholder is a no-op after the rewrite) and a merge could lose a parent; `'never'` keeps every commit and parent, so the commit-map is one to one (`_prunable`, `_maybe_trim_extra_parents`).
- SHAs in messages: filter-repo replaces each hex word of 7 to 40 digits that names an already rewritten commit by the new SHA of the same length (`_tweak_commit` runs the message callback first, then `_hash_re.sub(_translate_commit_hash, ...)`, unless `preserve_commit_hashes`). This design leaves the translation on. The root commit holds term hits, so OLD and NEW share no commit; with preserved hashes the newest `Done-Upto` trailer would name an OLD commit, `git merge-base` would find no common ancestor and every later `todo done` would stop with "Done-Upto … is not related" (`done.py` `_log`). Translated, `Done-Upto` values and the `(sha)` in `todo: done` subjects name NEW commits. A hex word that names no commit, or a prefix filter-repo cannot resolve uniquely, is left as is and listed in `.git/filter-repo/suboptimal-issues` (copied to D1, its count recorded). SHAs cited in files (todos, handovers, design docs) are blob text and stay as written.
- Identities and ref names have no hit (checked 2026-11-16) and get no callback; the scan proves it again.
- filter-repo removes the `origin` remote of `work/` (expected: "Why is my origin removed?"); step 8 adds the GitHub URL.

### D4 Preparation and pre-checks
`prep.py` runs on `mirror.git` and writes counts to `log.md` (line numbers, commit and path, never term text):
- `allow-candidates`: every distinct `key(line)` in any blob or message of history that holds the field term as a word, to a file in D1. Each is classified by hand: field (the carbon-copy field in code, config, test or a sentence about the field) or workspace. `ALLOW.txt` holds the field lines exactly. A mixed line (both uses) is left out, so its field mention is replaced too; such lines are prose in todos or notes, and their list goes into the review brief. The body records the file's path, line count and the count of mixed lines.
- `collisions`: for every commit of `git rev-list --all`, the `ls-tree -r -z` paths through `apply_path`; prints the number of commit trees where two old paths give one new path, and where two new paths differ only in case while their old paths did not (a Windows checkout would break). Both must be 0 before the rewrite; otherwise a placeholder is changed.
- `markers`: history lines holding `leakcheck:allow` and a term other than the field term (they would stay unchanged, D2). Must be 0; otherwise the owner decides before the rewrite.
- `terms`: every term `leakcheck history` reports on the mirror has a line in `map.txt`.

### D5 Checker
`python -X utf8 check.py OLD NEW` in `work/`, reading bytes with `git -c core.quotePath=false` and `cat-file --batch` (no textconv), prints one count per line, each must be 0:
1. `commits`: `git rev-list --count --all` in `mirror.git` minus the same in `work/`.
2. `pruned`: commit-map lines (after the `old new` header) whose new id is all zeros.
3. `trees`: for each commit-map pair, `ls-tree -r -z` of old and new; each old entry `(mode, path, blob)` must appear in the new tree as `(mode, apply_path(path), id)` where `id` is `sha1(b"blob %d\0" % len(c) + c)` of `c = apply_text(content)`, or the old blob id itself for a binary blob (the same test as `blob_callback`: a NUL in the first 8192 bytes), and the new tree must have no other entry; entries are compared as sorted lists, never sets. Symlinks (mode 120000) are blobs and go through `apply_text` like files; gitlinks (mode 160000, none expected) are compared as is and never read. Expected ids are cached per old blob, so each blob is read once.
4. `messages`: for each pair, `apply_text(old message)` against the new message: both are cut at filter-repo's hex-word pattern `\b[0-9a-f]{7,40}\b` (bytes); the pieces between hex words must be equal and the hex words pair one to one, each new word either equal to the old one or the same-length prefix of the new id the commit-map gives the unique old commit the old word starts. An unchanged word is accepted only when it names no rewritten commit or its prefix is ambiguous (filter-repo resolves prefixes among the commits processed so far, the checker among all, so an unchanged word ambiguous only in the full map is counted separately and listed for a look, not as a mismatch); a word that names exactly one rewritten commit and is unchanged counts as a mismatch, so a translation that did not happen fails here.
5. `allow`: ALLOW lines that `apply_text` changes.
6. `done-upto`: the newest `Done-Upto:` trailer on `work/` master that does not resolve to a commit of `work/` master (`done.py` `_watermark` lookup). Older unresolved values are printed as a count only.
7. `allow-kept`: ALLOW lines found in a mirror blob but in no blob of `work/` (`rev-list --objects --all` plus `cat-file --batch`; acceptance 5(c)).
`git diff -U0 --no-renames --no-textconv --no-ext-diff OLD NEW` (acceptance 5(a)) stays as an extra, over the paths present in both trees: removed lines hold a term, added lines none. Paths the rules rename show as a whole-file delete and add there, and are covered by count 3 instead.

`check.py` imports the same `rules.py` as the rewrite, so a matcher bug that leaves a term (for example a leftmost shorter match eating the start of an overlapping multi-word term) passes the checker; the independent Go scan of acceptance 4 is the check for that, and it suffices.

Encoding and signatures: filter-repo re-encodes messages with an `encoding` header to UTF-8 by default and drops `gpgsig`. `prep.py` records the counts of commits with an `encoding` header and with `gpgsig` in the mirror; both are expected 0, and a non-zero count is resolved (by `args.reencode` or by accepting dropped signatures with the owner) before the rewrite, never by loosening count 4.

`check.py --selftest` builds `fixture/` (a small repo with fake terms in a blob, a path, a message, a `Done-Upto` trailer naming its parent, an ALLOW line and a binary blob), rewrites it with `rewrite.py` and the fixture map, and expects all counts 0; then rewrites fresh copies wrongly (a blob callback that drops one line, a filename callback that skips one path, a message callback that skips one message, `prune_empty` left `'auto'` on a commit that becomes empty, `preserve_commit_hashes` set) and expects the matching count to be non-zero each time. A self-test that passes a wrong rewrite fails.

### D6 Run order
1. Prerequisites: EG0030, EG0049, EG0058 done; every other worktree and branch gone; terms file gains EG0049's search value; filter-repo installed (D9).
2. `todo move EG0048 in-progress --evidence branch=master`, then `in-review`, `todo commit`, `git push origin master`: the in-review state is part of OLD.
3. `state.py snapshot`: the acceptance 1 commands and outputs with UTC time into `log.md` and `state-start.json`. From here no commit in the main checkout and no pending todo write is committed.
4. `git clone --mirror`, `state.py backup` (acceptance 2: fsck, rev-parse master = OLD, the dry-run push, the three `gh api` queries). Build `map.txt` and `ALLOW.txt`; `prep.py allow-candidates|collisions|markers|terms`; `rules.py --selftest`; `check.py --selftest`.
5. Mirror scan: `leakcheck history -root C:/dev/apps/external_gateway -allow ALLOW.txt` in `mirror.git` lists hits per term.
6. `work/` clone, `rewrite.py`, copy `commit-map` and `suboptimal-issues`, `check.py OLD NEW`, scan of `work/`, go build and go test (acceptance 3-5). Rehearsal in `dry/` (D8). `git -C work diff --quiet OLD NEW -- <each path git status --porcelain listed in the start record>` exits 0 (those are the uncommitted `.ai/shared/` files; `reset --keep` in step 9 refuses when one of them differs between OLD and NEW).
7. Review lanes on the evidence (D8); owner sign-off on the review output (deploy flow).
8. `state.py compare` (master and ls-remote still OLD); `git -C work remote add origin URL`; `git -C work push --force-with-lease=master:OLD origin master`; `state.py remote` (ls-remote shows NEW, no other branch); fresh clone of URL, scan exit 0 (acceptance 6).
9. Main checkout: `git rev-parse master` = OLD and `git ls-remote origin refs/heads/master` = NEW re-checked (else stop, D7); `git fetch origin`; `git reset --keep origin/master`; `state.py compare` (status as in the start record); link.py; todo check; scan (acceptance 7).
10. Record the todo moves and done (D8).

### D7 Failures
- Any check in steps 4-7 fails: fix `map.txt`, `ALLOW.txt` or `rules.py`, delete `work/` and `dry/`, redo step 6; origin is untouched.
- Master or origin moved after step 3 (`state.py compare` differs): restart at step 2.
- Lease rejected at step 8: origin moved; nothing was written; restart at step 2.
- A single-ref push either lands or not: ls-remote shows OLD (retry step 8) or NEW (continue).
- Half-done (origin NEW, main checkout still OLD, e.g. the session ended after step 8): step 9 runs as written; its re-check proves both. If master is not OLD (a commit landed after step 3), stop and ask the owner: that commit must be re-applied on NEW by hand, never pushed on top of OLD.
- A check after the push fails: stop and ask the owner; restore (`git -C mirror.git push --force URL OLD:refs/heads/master`) only on the owner's word.

### D8 Todo flow
- `branch=master`: the start condition forbids any other branch. `todo done` does not check a `branch` that names the done branch (`.ai/shared/rules/todo.md`, `todo done` paragraph; DESIGN.md §4.3, TD0332), so the moves committed after NEW on master do not make EG0048 "moved past test.head".
- Merged signal: `todo done` needs a non-`todo:` subject naming EG0048 past the watermark or, by the rescan, after the commit that added `todo/EG0048.md`; the design commits (`EG0048: design …`) are such subjects and survive the rewrite unchanged (they hold no term). The watermark is the translated `Done-Upto` (D3), checked by `check.py` count 6.
- Review lanes (deploy risk high: the core map's high-risk panel) read this section list, `check.py` and `prep.py` output, the scan outputs, `rules.py`, the counts of `ALLOW.txt` and its mixed-line list, and the diff `OLD..NEW` in `work/`; they never see `map.txt` terms in their output.
- Rehearsal (step 6): `git clone work dry`, then in `dry/` the step-10 moves (`reviewed --evidence review.head=NEW`, `in-testing`, `tested` with `test.head=NEW`) and `todo done --no-fetch` without `--apply`; it must print `done EG0048`. `dry/` is never pushed.
- After the push: `todo move EG0048 reviewed --evidence review.head=NEW`, `in-testing`, `tested` with `test.head=NEW`, `test.command` = the scan command on the fresh clone, `test.line` its count line, then `todo commit`, `git push`, `todo done --apply`.

### D9 Installing git-filter-repo
`python -m pip install --user git-filter-repo==2.47.0`, installed 2026-11-16 on the owner's OK (module `%APPDATA%/Python/Python314/site-packages/git_filter_repo.py`, `git-filter-repo.exe --version` prints `a40bce548d2c`). The user site is not on PATH and `python -I` ignores it, so the drivers run as `python -X utf8 rewrite.py` and the version is read with the exe's full path. Verified in its source: `RepoFilter(args, filename_callback, message_callback, ..., blob_callback, ...)`; the message callback serves commits and tags; the blob callback is called as `(blob, metadata)` after the `--replace-text` step, which skips blobs with a NUL in the first 8 KB; `prune_empty` and `prune_degenerate` take `'never'`; a non-fresh clone aborts unless `--force`; `origin` is removed after the run.

## Test plan
Every item writes its command and output to `log.md`.
1. Start record: `state.py snapshot` (acceptance 1).
2. Backup: `state.py backup` (acceptance 2).
3. Rewrite: `rules.py --selftest` and `check.py --selftest` pass; `prep.py` counts 0; `python -X utf8 rewrite.py` exit 0; commit-map and `suboptimal-issues` copied.
4. Scan: `leakcheck history` on the mirror (hits listed per term) and on `work/` (exit 0, count line 0 hits): the recorded test.
5. Tree and allow-list: `check.py OLD NEW` prints 0 on all seven lines; `git diff -U0` extra; `go build ./...`; `go test` of the changed packages and importers. Rehearsal in `dry/` prints `done EG0048`; `git diff --quiet` over the start record's modified paths exits 0.
6. Push: `state.py compare` before, `state.py remote` after; fresh clone scan exit 0.
7. Main checkout: master = OLD and ls-remote = NEW before the move; `state.py compare` after it; `link.py`; `todo check`; scan exit 0; `git merge-base --is-ancestor`.
8. Purge decision: body line present.

## Not changed
- No tracked code changes in EG0048 (the scan tool is EG0058). Author and committer identities, ref names other than master, and other repos are untouched.
- SHAs cited in files (todos, handovers, design docs) stay as written; the commit-map translates them. SHAs in commit messages are translated by filter-repo (D3).
- The main checkout's old objects and reflogs stay local (owner-only `git reflog expire` and `git gc`).
- `refs/pull/*` on GitHub keep old SHAs until the owner's purge request.

## Design review record
Round 1 (two lanes: code+scope, tests+edges) FAILED with six blocking findings, fixed in round 2's revision:
- B1 filter-repo rewrites SHAs in messages: fixed by checking the translation exactly (D5 count 4) and keeping it on, not by `preserve_commit_hashes`, since preserved hashes would break `todo done` (D3); `Done-Upto` covered by count 6 and the rehearsal.
- B2 matcher parity with leakcheck: D2 (decode, Go lower-casing and word runes, ALLOW parse, marker lines, self-test with non-ASCII neighbours).
- B3 ALLOW lines hiding other terms: refused by `rules.py`; marker lines by `prep.py markers`.
- B4 path collisions: `prep.py collisions` (also case-only), and D5 compares sorted lists.
- B5 diff-based pairing: replaced by the commit-map tree walk (D5 count 3); `git diff -U0` kept as an extra.
- B6 pruning: `'never'` for both (D3); D5 counts 1 and 2.
Notes applied: binary blobs skipped (D3), bytes read without textconv or quoting (D5), widened self-test (D2), negative checker self-test (D5), named scripts (D6), step 9 re-check and half-done path (D6, D7), material kept until done (D1), todo flow cited and rehearsed (D8), origin removal (D3).

Round 2 (one verifier with the ledger and the diff) PASSED: B1-B6 and the notes fixed, B1's translation-on choice confirmed against filter-repo and done.py. Its eight notes were applied after the pass: unchanged hex words naming a rewritten commit count as mismatches and the self-test gains a `preserve_commit_hashes` case (D5 count 4); checker-wide prefix ambiguity listed, not failed (D5 count 4); `git diff -U0` pinned to `--no-renames` over shared paths (D5, acceptance 5(a)); binary rule, symlinks and gitlinks in the tree walk (D5 count 3); the Go scan named as the check for matcher bugs, overlapping multi-word terms included (D5); encoding-header and `gpgsig` counts (D5); `git diff --quiet` of the start record's modified paths before `reset --keep` (D6 step 6). Rejected: none.
