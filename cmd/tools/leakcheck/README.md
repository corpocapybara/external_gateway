# leakcheck

A git hook that refuses a commit adding the name of a real workspace, project, customer or internal host
(EG0047; design `docs/design/EG0047-leakcheck-hook.md`). The repo is public, so these names belong only in
gitignored files (`.ai/LOCAL.md`, `config.<x>.yaml`).

## Install

From the main checkout, once per clone and again after changing this tool:

```powershell
& "C:\Program Files\Go\bin\go.exe" run ./cmd/tools/leakcheck install
```

(or `go run ./cmd/tools/leakcheck install` with Go on PATH). It copies the checker to `.git/hooks/leakcheck.exe` and writes `.git/hooks/pre-commit.local` and
`commit-msg.local`, which the todo tool's managed `pre-commit` and `commit-msg` hooks run first. The hooks dir
is shared by every worktree. `install` refuses when `core.hooksPath` is set or when a `.local` hook it did not
write is in the way.

## Terms

`.ai/leakcheck-terms.txt` (gitignored, next to `.ai/LOCAL.md`): one term per line; blank lines and lines
starting with `#` are ignored. Without this file every commit is refused (fail closed); an empty file is fine.
Add to it the real workspace names, project keys, company names and domains.

The hosts of all `http://` / `https://` URLs in the main checkout's `config.*.yaml` are terms too, except a
built-in list of public service hosts (`publicHosts` in `leakcheck.go`) and loopback hosts.

Matching ignores case, and a term edge that is a letter, digit or `_` must be at a word boundary.

## What is checked

- Each line the commit adds (not unchanged or removed lines), with its new-file line number.
- The new path of each added, renamed or copied file.
- The commit message (`#` lines and everything below the scissors line are ignored).

Pre-commit skips while a merge is in progress (both sides were checked when committed).

## Deliberate false positives

Put `leakcheck:allow` on the line (in a comment, or in the message line). Paths have no marker. Git's own
`--no-verify` skips all hooks.
