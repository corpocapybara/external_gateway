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

## History scan

`leakcheck history [-root DIR] [-allow FILE] [REV...]` (EG0058) scans a whole repository, bare or not, for the
terms: every commit of `git log REV...` (default `--all`), plus all tags and refs. Surfaces:

- `blob`: each line of each file in each listed commit's tree (binary read as text; notes are blobs too). Each
  distinct blob is read once and attributed to the first commit and path that listed it. Gitlinks are skipped.
- `path`: each distinct full path (directory names included), attributed to the first commit holding it.
- `message`: each line of each commit message.
- `identity`: author and committer name and email (not mailmapped).
- `tag`: message lines and the tagger name and email of each annotated tag under `refs/tags`.
- `ref`: each ref name from `git for-each-ref`.

Tags and refs are scanned whatever REV is. Terms are read from `.ai/leakcheck-terms.txt` plus config hosts of
`-root DIR`; without `-root` that is the main checkout (the parent of the git common dir), so a bare repo
outside a checkout needs `-root`. A missing terms file is an error. `-allow FILE` lists line texts to skip: a
line (any line surface) whose trimmed text equals a trimmed FILE line (`#` and blank lines ignored) has no hits;
`leakcheck:allow` on a line works too. Paths, identities and ref names have no allow rule.

Output on stdout, one line per hit, terms joined by `, `:

```
leakcheck: blob <commit> <path>:<line>: <terms>
leakcheck: path <commit> <path>: <terms>
leakcheck: message <commit> line <n>: <terms>
leakcheck: identity <commit> author|committer: <terms>
leakcheck: tag <refname> line <n>: <terms>
leakcheck: tag <refname> tagger: <terms>
leakcheck: ref <refname>: <terms>
leakcheck: history: <N> hits in <M> commits
```

Exit 0 no hit, 1 a hit or any error (fail closed), 2 usage (bad flag, or a REV starting with `-`).

Limits: a ref pointing at a tree or blob is not walked, and a tag of a tag shows only the outer tag.

## Deliberate false positives

Put `leakcheck:allow` on the line (in a comment, or in the message line). Paths have no marker. Git's own
`--no-verify` skips all hooks.
