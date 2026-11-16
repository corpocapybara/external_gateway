# EG0047 design: Add GIT PreCommit Hook to sanitize data

## Decisions in brief
- D1: one stdlib-only Go command `cmd/tools/leakcheck` with subcommands `pre-commit`, `commit-msg FILE`, `install`; logic in testable functions taking a working dir and writers, `main` only dispatches (§Decisions D1).
- D2: the hooks run an installed copy `<common-dir>/hooks/leakcheck.exe`, never `go run` per commit; rerun `install` after changing the tool (§Decisions D2).
- D3: chain through the todo tool's `pre-commit.local` / `commit-msg.local` (LF `#!/bin/sh`, marker line, `exec` the exe by quoted forward-slash path); the managed hooks are not edited (§Decisions D3).
- D4: terms = gitignored `.ai/leakcheck-terms.txt` + hosts of http(s) URLs found as text in `config.*.yaml`, both from the main checkout; minus a built-in exact-host public list and loopback (§Decisions D4).
- D5: literal, lower-cased search; word boundary only at word-rune term edges; each term reported once per line, sorted (§Decisions D5).
- D6: content from `git diff --cached` with pinned flags, parsed as a state machine; paths from `--name-status -z`; binary and header lines never scanned (§Decisions D6).
- D7: pre-commit skips while `MERGE_HEAD` exists; commit-msg checks every message (§Decisions D7).
- D8: main checkout = parent of `git rev-parse --path-format=absolute --git-common-dir`; the diff uses git's own hook environment (§Decisions D8).
- D9: fail closed: any error exits 1 with `leakcheck: error: …`; a hit exits 1 with the hint line (§Decisions D9).
- D10: `install` refuses `core.hooksPath`, copies via temp file + rename, overwrites only its own marked files (§Decisions D10).
- D11: tests re-exec the test binary as the tool (`TestMain` dispatches subcommands), so no `go build` in tests; one shared temp-repo helper (§Decisions D11).
- D12: the live terms file is seeded locally from `.ai/LOCAL.md` and never committed (§Decisions D12).

## Acceptance
Restated from `todo/EG0047.md` (13 items): output format and exit codes of `pre-commit` (added lines,
path hits, hint, first commit, unchanged/removed lines ignored); literal, case-insensitive, word-bounded
matching; terms file plus config hosts minus public and loopback hosts; added/renamed/copied path hits,
binary skipped; `commit-msg` with `#`, scissors, CRLF, empty message; merge skip; `leakcheck:allow` marker;
worktree diff vs main-checkout terms; fail closed; `install`; scripted hook-chain end to end; `.gitignore` and
README; live seeded install refusing a commit. Each item names its test (§Test plan).

## Anchors
- `.gitignore`: section "# Local domain knowledge" (the `.ai/LOCAL.md` rule the new ignore line joins)
- `.ai/RULES.md`: heading "HARD RULE — no real workspace identifiers in tracked files"
- `cmd/tools/gen-token/main.go`: func main (the shape of an existing `cmd/tools` command)
- `todo/_config.yaml`: key `buckets` entry `cli-tools`
- `config.example/config.globex.yaml`: key `base_url` (where hosts live; the real `config.<x>.yaml` files share the shape)
- `go.mod`: key `module` (package path `github.com/external_gateway/cmd/tools/leakcheck`)

## Decisions
**D1 — one Go command.** `cmd/tools/leakcheck` (package `main`, stdlib only) next to the other tools. Files:
`main.go` (dispatch, exit codes), `leakcheck.go` (terms, matching, diff parsing, checks), `install.go`, tests.
Each subcommand is `func runX(dir string, args []string, stdout, stderr io.Writer) int` so tests call it with a
temp repo dir. Owner chose Go over a script (2026-11-16).

**D2 — installed binary, not `go run`.** `go run` at every commit would compile from whatever branch is
checked out (an older branch has no `cmd/tools/leakcheck`, a broken tree fails the hook) and costs a compile on
a cold cache. An installed copy in the shared hooks dir (`.git/hooks`, shared by all worktrees) is fast and
branch-independent. Trade-off: a changed tool needs `go run ./cmd/tools/leakcheck install` again; the README
says so.

**D3 — chain via `.local`.** The todo tool's managed `pre-commit` and `commit-msg` run `<name>.local` first and
stop on its failure (`"$LOCAL" "$@" || exit $?`; todo DESIGN.md 4.4 keeps foreign hooks as `.local`). The
wrappers:

    #!/bin/sh
    # leakcheck-hook: managed by cmd/tools/leakcheck install (EG0047)
    exec "C:/…/.git/hooks/leakcheck.exe" pre-commit "$@"

and the same with `commit-msg "$1"`. LF endings, mode 0755 (Git Bash treats a `#!` file as executable).

**D4 — terms.** `.ai/leakcheck-terms.txt` lives next to `.ai/LOCAL.md` (both gitignored, local knowledge). Lines
are trimmed (`strings.TrimSpace` also drops CR), a leading UTF-8 BOM is removed, blank and `#` lines are
ignored, terms are lower-cased and de-duplicated. Hosts: every match of `https?://[^\s"'<>]+` in each
`config.*.yaml` (`filepath.Glob`, which already excludes `config.<x>.yaml.<ts>.bak`), parsed with `net/url`;
`Hostname()` (no port, userinfo or brackets), lower-cased; parse errors and empty hosts skipped. Text scan, not
YAML decode, so a broken config still yields its hosts. Exempt: the built-in exact-host list `publicHosts`
(`api.github.com`, `github.com`, `gitlab.com`, `api.bitbucket.org`, `bitbucket.org`, `api.trello.com`,
`trello.com`, `slack.com`, `api.slack.com`, `api.clockify.me`, `api.tempo.io`, `mcp.atlassian.com`,
`api.atlassian.com`, `docs.googleapis.com`, `oauth2.googleapis.com`, `www.googleapis.com`,
`graph.microsoft.com`, `login.microsoftonline.com`, `imap.gmail.com`, `smtp.gmail.com`,
`outlook.office365.com`) plus `localhost`, `127.0.0.1`, `::1`. Exact hosts, not domain suffixes: a tenant
subdomain of a public SaaS domain names the tenant and stays a term.

**D5 — matching.** Lower-case the line (`strings.ToLower`) and search each term with `strings.Index` in a loop
(literal: a `.` in a host is a dot). A hit counts when, for each term edge whose rune is a letter, digit or `_`
(`unicode.IsLetter/IsDigit`), the neighbouring rune in the line is not one (or is the line end). Each term is
reported once per line; terms are sorted so output is deterministic. Lines are split from the whole output
(`bytes.Split`), so no scanner token limit applies.

**D6 — what is scanned.** Base = `HEAD` when `git rev-parse --verify -q HEAD` succeeds, else the empty tree
(`git hash-object -t tree --stdin` with empty input, which also fits sha256 repos). Content:
`git -c core.quotePath=false diff --cached -U0 --no-color --no-ext-diff -M -C --src-prefix=a/ --dst-prefix=b/ <base>`.
Parser state: `diff --git` starts a file; `+++ b/<path>` sets the current path (`+++ /dev/null` = deleted: no
lines follow anyway); `@@ … +c[,d] @@` sets the new-file line counter to c; inside a hunk a `+` line is scanned
(and the counter advanced), `-` lines are not; `Binary files … differ` and every header line are skipped. A
quoted path (`"…"`) is unquoted with `strconv.Unquote`. Trailing `\r` is stripped before matching. Paths:
`git diff --cached --name-status -z -M -C <base>`; status `A`, `R*`, `C*` → the new path is checked; `M`, `D`,
`T` are not.

**D7 — merges.** A clean `git merge` never runs pre-commit; a conflicted one does, and its diff against HEAD
is the other side's whole change, already checked when it was committed. So pre-commit exits 0 while
`git rev-parse --git-path MERGE_HEAD` exists (same choice as the todo hook). Cherry-pick, revert and amend
bring new content into a commit and are checked. commit-msg has no skip: a merge subject naming a branch with a
term is a leak too.

**D8 — main checkout.** Git runs hooks at the worktree top with its own `GIT_DIR` / `GIT_INDEX_FILE`; the
tool runs its git commands in its cwd with the inherited environment, so the diff is the committing
worktree's index. Terms and configs come from `filepath.Dir(<--git-common-dir>)`, which is the main checkout for
both the main checkout (`<root>/.git`) and a linked worktree under `.worktrees/`.

**D9 — exit codes and messages.** 0 = clean or skipped. 1 = hits (stderr: one `leakcheck: …` line per hit, then
`leakcheck: commit refused (EG0047): remove the names above, or add leakcheck:allow to a line that is a
deliberate false positive`) or an error (`leakcheck: error: <what>: <err>`: missing/unreadable terms file named by
path, unreadable config, failing git command with its stderr). The terms file is loaded before anything is
diffed, so a missing file fails even with nothing staged. Usage errors exit 2.

**D10 — install.** Refuses (exit 1) when `git config --get core.hooksPath` is non-empty. Hooks dir =
`<common-dir>/hooks`. Copies `os.Executable()` to `leakcheck.exe.tmp` there, then `os.Rename` over
`leakcheck.exe` (replace-existing on Windows). A `.local` that exists without the marker line is a foreign hook:
refuse and name it; one with the marker is rewritten. Prints what it wrote.

**D11 — tests.** `TestMain` runs the tool when `os.Args[1]` is a subcommand name (normal test runs pass only
`-test.*` flags), so `install` copies the test binary and the end-to-end wrappers exec it without a build. One
helper `newRepo(t)` does `git init`, sets `user.name/email`, `commit.gpgsign=false`, `core.autocrlf=false`
and writes a terms file; tests keep git processes few.

**D12 — live seed.** The implementer writes `.ai/leakcheck-terms.txt` in the main checkout from `.ai/LOCAL.md`
section "Real names in tracked files" and "Placeholders used in committed todos" (workspace names, Jira keys,
company names, domains), runs `install`, and checks a refused commit; nothing of it is committed.

## Test plan
All in `cmd/tools/leakcheck`, `go test ./cmd/tools/leakcheck/`:
- Output and exit → `TestPreCommitBlocksAddedLine`, `TestPreCommitIgnoresUnchangedAndRemovedLines`, `TestPreCommitFirstCommit`
- Matching → `TestMatch`
- Terms → `TestLoadTerms`
- Paths, binary → `TestPreCommitPaths`, `TestPreCommitBinary`
- commit-msg → `TestCommitMsg`
- Merges → `TestPreCommitSkipsMerge`
- Marker → `TestAllowMarker`
- Worktrees → `TestWorktreeUsesMainCheckout`
- Fail closed → `TestFailClosed`
- Install → `TestInstall`
- Hook chain → `TestHookChainEndToEnd`
- `.gitignore` and README → `TestRepoWiring`
- Live → manual e2e in the main checkout (`test.kind=e2e`)

## Not changed
- The todo tool's managed hooks and `todo install-hooks`.
- Existing mentions in the tracked tree and history (EG0038, EG0048).
- Any server-side or CI check; gateway code under `internal/`.
