---
date: 2026-11-14
author: agent
todos: [EG0039, EG0040]
branches: [EG0039]
sessions: []
---
# Handover: hotmail-xoauth2-parked (2026-11-14)
## Done
EG0039 refined after 5 refinement rounds (R4 and R5 by owner decision; R4 fix: SMTP XOAUTH2 Next returns []byte{} on 334, IMAP fake keeps the connection open after NO). Parked at refined on the owner's word ("park it on refinement").
EG0040 designed with design=none (refinement R3 verifier PASS; live config, secrets and deploy only).
EG0039 design: docs/design/EG0039-hotmail-xoauth2.md on master (8a4f9ae); design round 1, two lanes, FAIL with 2 blocking findings, both fixed in e0e626f (example-config test via a temp copy with a placeholder hash; no-token-in-error assertions) plus 18 notes folded in.
## Left, in order
EG0039 design round 2: one verifier on the round-1 model, about 40 tool calls and 500 words, given the round-1 findings (todo/EG0039.md body, "Parked at refined") and git diff 8a4f9ae e0e626f -- docs/design/EG0039-hotmail-xoauth2.md; on PASS: todo move EG0039 designed --evidence design=docs/design/EG0039-hotmail-xoauth2.md with design_review evidence.
EG0039 implement in .worktrees/EG0039 (branch EG0039, WIP commit d650f71, untested draft: xoauth2.go and connector.go; connector_test.go still uses SmtpTLS; IMAP no-TLS XOAUTH2 guard missing), then high-risk review panel, tests (baseline on master: email 3.3 s, server 51.4 s, tokencheck 3.3 s, config 1.5 s, policy 3.7 s), --no-ff merge, push, todo done --apply, remove worktree and branch.
EG0040 after EG0039 is done: live hookup with the owner as in the older handover hotmail-xoauth2_2026-11-14.md.
## Open sessions and agents
none
## Next briefs
Resume only on the owner's word: todo assign EG0039; read the EG0039 body ("Parked at refined"), the design's Decisions in brief, then run design round 2 as above.
## Key files
todo/EG0039.md, todo/EG0040.md
docs/design/EG0039-hotmail-xoauth2.md
.worktrees/EG0039 (branch EG0039)
.scratch/EG0039/ (refinement ledger and round diffs)
