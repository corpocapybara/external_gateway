# Conventions & Rules

## HARD RULE — no real workspace identifiers in tracked files

Committed/tracked files (all `.ai/*.md` except `LOCAL.md`, `docs/*.md`, `README.md`,
`CLAUDE.md`, source, and the `config.example/` templates) **must never** contain a real
workspace name, port, host/domain, deploy path, or token. Use generic placeholders
instead: `<ws>` for the workspace token, `<port>` for its port, `<name>` for a secret,
`<agent-token>` for a token — or the illustrative example names `acme`/`globex`.

Real identifiers (e.g. actual workspace tokens, their pinned ports, internal URLs,
Credential Manager paths, agent tokens) live **only** in `.ai/LOCAL.md`, which is
gitignored. If you need a concrete example, put it in `LOCAL.md` and reference it from
the tracked doc — never inline the real value.

Before committing docs, grep for leaked identifiers (`\bcc\b`, real ports, internal
domains). A tracked file that names a real workspace is a bug, even if the surrounding
prose is correct.

## Code style

- **No comments** in Go source unless a pattern genuinely needs explanation. Let the code speak.
- **Error wrapping**: always use `fmt.Errorf("context: %w", err)`. Never use `errors.Wrap`.
- **Imports**: standard library first, then third-party, separated by blank line.
- **Logging**: use `s.logger.Info()/.Error()/.Debug()` with structured fields (`.Str()`, `.Int()`, `.Err()`). Never use `fmt.Println` for operational logging.
- **Secrets in `[]byte`**, never `string`. This allows zeroing after use.
- **Audit logs vs app logs**: `audit.LogAllow()`/`LogDeny()` for operational audit trail; `zerolog` for debugging.

## Workspace convention — `config.<x>.yaml` is the single source of truth

A workspace is defined solely by adding a `config.<x>.yaml` (+ `operations.<x>.yaml`).
The token `<x>` is an opaque label (e.g. `globex`, `acme`) and **everything else derives
from it** at deploy time:

| Derived | From `<x>` |
|---------|-----------|
| Deploy dir | `<InstallDir>\<x>\` |
| Windows service | `egw-<x>` |
| Ops file | `operations.<x>.yaml` |
| Credential Manager namespace / secret refs | `egw/<x>/<name>`, `secret://<x>/<name>` |
| HTTP API path | `/v1/workspaces/<x>/op/<id>` |
| Port | `8443 + (alphabetical index)`, unless the config sets `server.port` |

`deploy/workspaces.ps1` discovers every `config.<x>.yaml`, assigns ports
alphabetically (base 8443), lets any config override with an explicit
`server.port`, and **aborts on a port conflict**. `deploy/provision.ps1` and
`deploy/update.ps1` loop over the discovered set (roles and secret names are read
from each config too) — so add or remove a workspace just by adding/removing its
`config.<x>.yaml`.

> Because unspecified ports are alphabetical, adding a workspace can shift the
> ports of later-sorted ones. Pin `server.port` in each config if you need
> stable ports across changes.

## Config rules

- **Config is the source of truth.** The deploy script copies config files verbatim — no sed-like patching.
- **Secret references** use `secret://<workspace>/<name>` format. Never put plaintext secrets in config.
- **Operations are separate from config**: `operations.<ws>.yaml` defines what operations exist; `config.<ws>.yaml` defines workspaces, sites, roles, and agents.
- **YAML keys use underscores**: `base_url`, `secret_ref`, `body_template`, `allow_fields`. No camelCase in config keys.
- **Role operations** list must only reference operations that exist in the operations file. Config validation checks this.

## Adding operations — checklist

1. Add operation definition in `operations.<workspace>.yaml`
2. Ensure the site (`site:`) exists in `config.<workspace>.yaml` with the right environment
3. Add the operation ID to the role(s) that should be allowed to use it
4. If read-only, add `constraints: - type: readonly`
5. If it requires a tunnel, ensure the environment has `requires_tunnel` set
6. Build, deploy (as Admin), restart service, test

## Adding a new connector — checklist

1. Implement `connectors.Connector` interface in `internal/connectors/<name>/connector.go`
2. Wire the connector into `server.go`'s operation dispatch (add a case for the connector name)
3. If it resolves secrets, use `secrets.ResolveWithWorkspaceCheck()` for cross-tenant enforcement
4. Register taint after use: `secrets.GetTaintRegistry().Taint(secret)`
5. Audit all responses with `s.audit.LogAllow()` or `LogDeny()`

## Security rules

- **Never log secrets.** The redaction hook scans for tainted values, but don't rely on it — just don't log `[]byte(secret)` values.
- **Tunnel checks are detect-only.** The gateway never auto-connects or brings up VPNs. It reports status and denies operations that require a down tunnel.
- **Postgres is READ ONLY.** The SQL guard enforces this. Don't weaken the regex or add write operations without serious consideration.
- **Cross-tenant isolation**: the secret resolver refuses `secret://` references whose workspace prefix doesn't match the request's workspace.

## Go-specific rules

- **`sync.Once` for singletons**: resolver, taint registry, audit logger use `sync.Once` with package-level initialization functions.
- **Context-aware HTTP calls**: always use `http.NewRequestWithContext(ctx, ...)` — connectors receive context from the server.
- **Deferred close**: always `defer resp.Body.Close()` after HTTP calls.
- **No `panic()`**: all errors are returned or logged via `zerolog.Fatal()` (only at startup for unrecoverable errors).

## PowerShell rules (deploy scripts)

- Always use `-Encoding UTF8` with `Set-Content` and `Get-Content`
- Check if running as Admin: `[Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)`
- Use `-Force` on `Copy-Item` to overwrite existing files
- Use `Start-Sleep` after service stop to let processes exit before copying binaries
- Use `-ErrorAction SilentlyContinue` on service start/stop to avoid script termination if service is already in that state
