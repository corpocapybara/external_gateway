---
date: 2026-11-14
author: agent
todos: [EG0039, EG0040]
branches: []
sessions: []
---
# Handover: hotmail-xoauth2 (2026-11-14)
## Done
EG0039 refined text written (draft): 11 acceptance items, 3 critic rounds; the post-round-3 fix (IMAP auth rejection decided by connection state, LoggedOut() still open) awaits the owner's OK.
EG0040 created by owner request (live hookup in cc, blocked by EG0039); acceptance written, round 1 + round 2 fixes applied; needs round 3 (verify only: /admin/secrets/status route, cc admin Bearer token, refresh-token rotation clause).
Owner answers 2026-11-14: IMAP/SMTP XOAUTH2 in the email connector (not Graph); ops read + send; workspace cc; live hookup is EG0040; app registered in the owner's Entra directory.
Entra app registered via Chrome with the owner's OK (personal accounts only, public client flows on, no secret); the owner consented. Ids, address and directory are in .ai/LOCAL.md section "cc workspace: Hotmail" (gitignored, never commit them).
Premise proven by a live probe: device code on /consumers; IMAP outlook.office365.com:993 XOAUTH2 OK; SMTP smtp-mail.outlook.com:587 STARTTLS + XOAUTH2 235; refresh without scope OK and rotates the refresh token.
## Left, in order
EG0039 open owner question (watchdog a-20261114-100112-d06f): accept the post-R3 fix without a 4th round? On yes: move EG0039 to refined with a note (3 rounds, 2 lanes in R1, rejected finding: tokencheck caller partly, risk: high (connector auth, credential refresh/storage, cross-bucket), design: needed (new auth mode across config/connector/server; seam and transport choices), owner acceptance of the R3 fix).
EG0040 refinement round 3 (verifier, diff since 7e10af3), then refined; design: not needed (live config only), so designed with design=none and the reason.
EG0039 design step: docs/design/EG0039-hotmail-xoauth2.md is drafted (UNTRACKED in the main checkout; stage/commit it before linking with todo set design=...). Run the design critics (two lanes, M/high risk), check Decisions in brief <= 2,000 chars and every Anchor resolves, then designed.
EG0039 implement in .worktrees/EG0039 (branch EG0039), deliver per todo.md "Who moves them": review lanes = high-risk panel (all four lanes + adversarial pass), tests (affected packages: email, server, tokencheck, config, policy; report wall time before/after), --no-ff merge to master, push, todo done --apply, remove worktree and branch.
EG0040 with the owner: device-code tokens into egw/cc/hotmail + hotmail-refresh via /admin/secrets (cc admin token), live config + ops, update.ps1 elevated by the owner, diag, e2e (owner approves the exact test mail), live refresh check; record test.kind=e2e.
## Open sessions and agents
none
## Next briefs
Fresh session brief: deliver EG0039 then EG0040 from this note; first `todo assign EG0039` and `todo assign EG0040`; read both todos, .ai/shared/rules/todo.md and .ai/LOCAL.md "cc workspace: Hotmail"; ask the owner the open question first if the watchdog has not resolved it.
## Key files
todo/EG0039.md, todo/EG0040.md
docs/design/EG0039-hotmail-xoauth2.md (untracked draft)
.scratch/EG0039/r1-ledger.md (round-1 finding ledger)
internal/connectors/email/connector.go, internal/server/server.go (executeEmailConnector, refreshOAuthToken, admin diag), internal/tokencheck/tokencheck.go (DetectKind)
config.example/config.globex.yaml, config.example/operations.globex.yaml
.ai/LOCAL.md (gitignored; real ids)
