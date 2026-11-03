# Security Policy

## Security Model

external_gateway is a credential proxy: agents authenticate with a Bearer token,
the gateway resolves the actual upstream credentials from the **Windows Credential
Manager** at runtime, and the agent never sees them.

- **Secrets never touch disk.** Credentials live in `LocalSystem`'s Credential
  Manager, namespace `egw/<workspace>/<name>`. No secrets are stored in config
  files or logs.
- **Agent tokens are hashed.** Config stores `token_sha256` (SHA256 of the bearer
  token). The raw token value is generated once, stored in `agent-keys.txt`
  (gitignored), and cannot be recovered from the config.
- **Admin API can be locked down.** Set `admin_token_sha256` in config to require
  SHA256-authenticated bearer tokens on all `/admin/*` endpoints.
- **Cross-tenant isolation.** Every secret resolution enforces that the secret's
  workspace prefix matches the request's workspace. A `secret://globex/...` ref
  cannot be resolved during an `acme` workspace request.
- **Taint tracking.** Resolved secrets are registered in a taint registry, with
  their trimmed, JSON-escaped and URL-encoded forms (secrets of 4 bytes or fewer
  are not tracked). Responses and audit-logged values are redacted before they
  leave the gateway. The audit log also masks parameters whose key looks like a
  credential (`token`, `secret`, `password`, `authorization`, ...) at any depth.
- **PostgreSQL is read-only.** Queries run in a `READ ONLY` transaction and are
  parsed to an AST; with `pg_allowed_tables` set, every table reference and a
  deny-list of SQL-executing / server-touching functions are checked (fail closed).
  The deny-list does not cover user-defined or `SECURITY DEFINER` functions:
  restrict those with database grants.

## Accepted risks

- **Re-deploy trusts the checkout** (owner decision, 2026-11-03). egw-desktop's
  elevated Re-deploy runs `deploy\update.ps1` from the checkout with `-File` and
  copies `egw.exe` and the `config.*.yaml` / `operations.*.yaml` files into
  Program Files, where `egw.exe` runs as `LocalSystem`. Anything that can write
  the checkout therefore gets SYSTEM at the next deploy. Embedding the script in
  egw-desktop was tried and reverted: it closed only the script path while the
  binary and configs stayed open, and it added a rebuild after every script
  edit. Don't propose that fix alone; a real fix builds or signs `egw.exe` in
  an admin-only location, and that is the owner's call.

## What is committed

This repository ships **templates and placeholders only**. No real credentials,
hostnames, or customer identifiers are committed. See `.gitignore` for the full
list of ignored patterns (config files, operation catalogs, token files, secrets).

## Reporting a Vulnerability

If you find a security issue, please report it privately by opening a GitHub
Security Advisory or contacting the repository maintainer directly. Do not file
a public issue.

## Supported Versions

Only the latest commit on `master` is supported. There are no release tags or
versioned releases.
