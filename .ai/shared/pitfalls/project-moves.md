# Moving project directories on Windows

Renaming/moving a project folder is never just a rename. Everything below was learned the hard
way while restructuring `C:\dev` into `pokemon/ lego/ games/ apps/ platforms/ lab/ shared/`
(2026-10-26). `_tools\scripts\preflight-move.ps1` checks most of it; run it before the move.

## What survives a move, what breaks

| Thing | After a same-volume move | Note |
|---|---|---|
| File contents, git history, `.git` | survive | a move is an inode rename |
| **Hardlinks** (`C:\dev\.ai\*` → project `.ai/shared/*`) | **survive** | same volume, same inode; `link.py` keeps working |
| **Junctions/symlinks inside** the moved dir | survive (absolute targets) | but they now point at the *old* absolute location |
| **Junctions elsewhere pointing INTO** the dir | **break** | this is the dangerous case — `bun`/`npm` `node_modules` links, `~/.claude/hooks` |
| Hardcoded paths in configs | break silently | `opencode.jsonc`, `.mcp.json`, `.claude.json`, `.ai/links.json`, agent prompt `{file:...}` refs |
| opencode project/session identity | break | storage is keyed by directory, see below |
| A running process with the dir as **CWD** | move fails | "Process cannot access the file" even though every file inside is movable |

### When the directory itself is locked
The *contents* are still movable even when the directory is not:

```powershell
New-Item -ItemType Directory -Path $dst -Force
Get-ChildItem -LiteralPath $src -Force | ForEach-Object {
  Move-Item -LiteralPath $_.FullName -Destination (Join-Path $dst $_.Name) -Force
}
# the now-empty source stays locked until that process exits
```

## opencode / Claude Code keep their own indexes

- **opencode**: `~/.local/share/opencode/opencode.db` (SQLite, can be tens of GB).
  `project.worktree`, `project_directory.directory` and `session.directory` hold absolute
  directories. Fix with `_tools\scripts\opencode-paths-fix.mjs` (dry-run by default) and confirm
  with `opencode-paths-verify.mjs`. Update children **before** parents, or a prefix mapping
  swallows the child paths. Do not `select *` the `message`/`part` tables — 12 GB DB, node runs
  out of memory.
- **Claude Code**: `~/.claude.json` → `projects` map keyed by directory.
  `_tools\scripts\claude-json-clean.mjs` drops entries whose directory is gone. Note the file can
  contain keys differing only in case (`c:/dev/locai` vs `C:/dev/locai`), which makes
  PowerShell's `ConvertFrom-Json` fail outright — use node to parse it.

## PowerShell 5.1 traps that cost real time

- **A BOM-less `.ps1` is read as ANSI.** Non-ASCII characters (Polish letters, `…`, `→`) turn
  into mojibake and can break the parser. Keep scripts ASCII-only, or save with a BOM.
- **`git ls-files` C-quotes non-ASCII paths** (`"…nieruchomo\305\233ci…"`), so scripts that
  iterate it skip those files without any error. Use `git -c core.quotePath=false ls-files`.
- **`Set-Location` does not change the .NET current directory.** `[System.IO.File]::ReadAllBytes('x')`
  still resolves against the process CWD. Use absolute paths with .NET APIs.
- `if` cannot be used as an expression inside `-f` formatting; build the value first.
- **`Set-Content -Encoding UTF8` writes a BOM** and JSON consumers reading strict UTF-8 (`json.load(open(p, encoding="utf-8"))`)
  then fail with `Unexpected UTF-8 BOM`. Write BOM-less with
  `[System.IO.File]::WriteAllText($p, $text, (New-Object System.Text.UTF8Encoding($false)))`.
- **Editing a hardlinked file forks it** (Edit/Write replace the file). After editing anything under
  `.ai/`, run `python -X utf8 C:/dev/.ai/link.py fix` and then `link.py` (check) — it reads a BOM-less `links.json`.

## Git traps

- **Husky / lint-staged `pre-push` hooks fail after a move** when `node_modules` junctions are
  dangling — the hook is not at fault and `--no-verify` hides a real breakage. Repair the links first.
- **A shallow clone cannot be pushed to a new remote**: `remote: fatal: did not receive expected
  object …`. `git fetch --unshallow <remote>` first (82 MB → 498 MB on `opencode`).
- **`core.autocrlf=true` + a repo with mixed line endings** produces `LF will be replaced by CRLF`
  warnings on nearly every commit. Noise, not corruption — but it makes `git status` look dirty.
- Untracking a file (`git rm --cached`) does **not** remove it from remote history. That needs
  `git filter-repo`/BFG + force-push, which rewrites every clone.

## Credentials, without installing new CLIs

`gh`/`glab` may be absent while `git` still has working credentials in Windows Credential Manager:

```powershell
$in  = "protocol=https`nhost=github.com`n`n"
$out = $in | git credential fill       # username=..., password=<token>
```

That token can drive the REST API (`POST /user/repos`, `POST /repos/x/y/forks`). Never echo it,
never write it to a file, and prefer `git` commands over API calls when both work.

## Storage: keep big payloads out of git, but not out of reach

Junctions let a repo keep its expected layout while the bytes live elsewhere (e.g. OneDrive):

```
repo\incoming  ->  C:\Users\<you>\OneDrive\<proj>\incoming
```

`pokemon-assets\setup-links.ps1` is the reference implementation. Treat the sync folder as
*sync*, not backup: a local delete propagates. Anything irreplaceable needs a second copy that
is not the sync target.
