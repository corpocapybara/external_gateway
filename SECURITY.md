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
- **Taint tracking.** Resolved secrets are registered in a taint registry. The
  redaction hook scans response data for tainted values before returning to the
  agent.
- **PostgreSQL is read-only.** Multi-statement detection and regex-based DDL/DML
  guards prevent write operations. Full AST parsing is not yet implemented.

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
