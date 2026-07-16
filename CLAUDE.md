# external_gateway

A compiled Go proxy that lets LLM agents call external services (Jira, GitLab, Kibana, PostgreSQL, Bitbucket, Clockify, Slack) without seeing credentials. Credentials live in Windows Credential Manager (`egw/<workspace>/<name>`), resolved at runtime by the gateway running as `LocalSystem`.

**Always start here:**
- `.ai/AGENTS.md` — build, deploy, secrets, admin API, architecture, conventions
- `.ai/INDEX.md` — full doc index with descriptions (lazy-load as needed)

**Before acting on this project, read `.ai/LOCAL.md` if it exists.** It holds workspace-specific domain knowledge (real URLs, tokens, deploy paths) that is gitignored and never committed. If absent, treat this project as a generic template with no specific deployment.

## Other AI harnesses

Link or copy the relevant files to your harness's config:

| Harness | Config location | Reference |
|---------|----------------|-----------|
| Cursor | `.cursorrules` | Point to `.ai/` docs (Cursor reads project rules from root) |
| GitHub Copilot | `.github/copilot-instructions.md` | Link to `.ai/` as instructional context |
| Continue.dev | `.continue/config.json` | Add `.ai/*.md` as `contextProviders` |
| Aider | `.aider.conf.yml` | Use `--read=.ai/AGENTS.md,.ai/INDEX.md` or add to `read:` list |
