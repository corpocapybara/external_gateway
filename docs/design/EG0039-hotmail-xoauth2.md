# EG0039 design: Connector to hotmail account (IMAP/SMTP XOAUTH2)

## Decisions in brief

- D1: `auth.type: oauth2` on an email site selects XOAUTH2; anything else stays basic. No new config key (§Decisions D1).
- D2: `email.Config` gets `AuthMode`, `SmtpImplicitTLS` (replaces `SmtpTLS`) and `RootCAs`; one `emailConfig(env, secret, rootCAs)` in the server builds it for ops and diag (§D2).
- D3: XOAUTH2 lives in `internal/connectors/email/xoauth2.go`: a go-sasl `sasl.Client` for IMAP and a `net/smtp.Auth` for SMTP; on a server challenge both answer an empty response; the SMTP Auth refuses to start without TLS (§D3).
- D4: SMTP: one `dialSMTP` for Send and VerifySMTP; implicit TLS iff `SmtpImplicitTLS`, else plain + mandatory STARTTLS before AUTH. `sendPlain` and the opportunistic path go (§D4).
- D5: `email.ErrAuthRejected` wraps a Login/Authenticate error only while `LoggedOut()` is open (checked before Logout) and it is no net/EOF/LOGINDISABLED error; SMTP: a 535 from AUTH (§D5).
- D6: server helper `withEmail(env, workspace, call)` runs the call; on ErrAuthRejected with oauth2 + `oauth` block it calls the unchanged `refreshOAuthToken` once, re-resolves the secret and retries once; a failed refresh returns `%w: OAuth token refresh failed` (§D6).
- D7: `Search` fetches `imap.FetchUid` and returns the Fetch error (§D7).
- D8: `tokencheck.DetectKind` returns `KindOAuth` also for `auth.type: oauth2` (§D8).
- D9: Concurrent refreshes and a failed rotated-refresh-token store keep today's behaviour; documented, not changed (§D9).
- D10: Tests run in-process on 127.0.0.1 with one generated ECDSA cert (SAN 127.0.0.1) per test package; no sleeps, one closed-port helper; wall time before/after reported (§D10).
- D11: Example config and ops in `config.example/*.globex.yaml`, agent hash a 64-zero placeholder (owner); `docs/HOTMAIL.md` plus INDEX, README and both PITFALLS entries; no real address or client id committed (§D11).
- D12: In acceptance (owner 2026-11-14): XOAUTH2 only over TLS (tested); SMTP deadline, Search Fetch error (untested) (§D12).

## Acceptance (restated, from todo/EG0039.md)

1. Config seams: `AuthMode`, `SmtpImplicitTLS`, `RootCAs`; server mapping; nil RootCAs in production; TestEmailConfigFromEnv.
2. IMAP XOAUTH2 exact string (trimmed token); basic keeps LOGIN; TestImapXOAuth2Login, TestImapBasicLoginUnchanged.
3. SMTP implicit TLS vs mandatory STARTTLS; AUTH XOAUTH2 / PLAIN; TestSmtpStartTLSThenXOAuth2, TestSmtpNoStartTLSRefused, TestSmtpImplicitTLS, TestSmtpBasicPlainUnchanged.
4. Auth rejection at wire level incl. the challenge handshake; TestImapXOAuth2Rejected, TestSmtpXOAuth2Rejected, TestDialErrorNotAuthRejected, TestImapConnectionClosedNotAuthRejected; no token in error text.
5. One helper for ops and diag; refresh once and retry once; failed refresh error; TestEmailOAuth2RefreshRetry.
6. Search UIDs; TestSearchReturnsUID, TestSearchEmptyMailbox.
7. Diag imap_login and smtp_login via the helper; TestDiagEmailOAuth2.
8. tokencheck DetectKind for oauth2 without oauth block.
9. Example config and ops; the example agent's empty `token_sha256` becomes a 64-zero placeholder; TestExampleHotmailConfig loads the file itself.
10. docs/HOTMAIL.md, INDEX, README, PITFALLS; no real values committed.
11. XOAUTH2 only over TLS (IMAP guard, SMTP Auth Start); SMTP deadline and Search Fetch error without tests; TestImapXOAuth2RefusedWithoutTLS, TestSmtpXOAuth2AuthRequiresTLS.
12. Affected packages' tests pass, wall time reported, fast in-process tests.

## Anchors

- internal/connectors/email/connector.go: type Config
- internal/connectors/email/connector.go: func dialImap
- internal/connectors/email/connector.go: func (c *Connector) Search
- internal/connectors/email/connector.go: func (c *Connector) ReadMessage
- internal/connectors/email/connector.go: func (c *Connector) Send
- internal/connectors/email/connector.go: func (c *Connector) VerifySMTP
- internal/connectors/email/connector.go: func sendPlain
- internal/connectors/email/connector.go: func sendTLS
- internal/server/server.go: func (s *Server) executeEmailConnector
- internal/server/server.go: func (s *Server) refreshOAuthToken
- internal/server/server.go: func (s *Server) handleAdmin (case "diag")
- internal/server/server.go: type Server (field oauthRefreshMu)
- internal/tokencheck/tokencheck.go: func DetectKind
- internal/tokencheck/tokencheck_test.go: func TestDetectKind
- internal/server/credential_status_test.go: func newCredHarness
- internal/server/credential_status_test.go: type credStore
- internal/connectors/email/connector_test.go: func unreachableCfg
- internal/config/config.go: func (c *Config) Validate
- .ai/INDEX.md: row "GMAIL.md"
- internal/secrets/resolver.go: func SetStore
- internal/secrets/resolver.go: func (r *Resolver) FlushCache
- internal/config/config.go: type Environment
- internal/config/config.go: type OAuthRefreshConfig
- config.example/config.globex.yaml: workspaces.globex.sites.email
- config.example/operations.globex.yaml: email.list_mailboxes
- .ai/PITFALLS.md: heading "Hotmail/Outlook consumer accounts require XOAUTH2"
- README.md: row "Hotmail/Outlook"
- go-imap v1.2.1 client/cmd_noauth.go: func (c *Client) Authenticate
- go-imap v1.2.1 server/server.go: func (s *Server) EnableAuth
- go-imap v1.2.1 status.go: func (r *StatusResp) Err
- go-imap v1.2.1 commands/authenticate.go: func (cmd *Authenticate) Handle
- Go stdlib net/smtp/smtp.go: func (c *Client) Auth

## Decisions

**D1 — selector.** The email site's existing `auth.type` already exists in `config.AuthConfig` and is `basic` for
Gmail. `oauth2` is a new value, read only by the email path and tokencheck; no YAML key is added. The access token
lives in `secret_ref` (as for http-rest bearer sites) and the refresh settings in the existing `oauth` block
(`OAuthRefreshConfig`), so `/admin/status` already lists the refresh and client secrets (admin_status.go `add` of
`oauth-refresh`). Alternatives: a new `email_auth:` key (rejected: duplicates `auth.type`), or Graph Mail via http-rest
(rejected by the owner, 2026-11-14: email policy would not apply).

**D2 — config and seams.** `email.Config` fields after the change: `ImapHost, ImapPort, ImapTLS, SmtpHost, SmtpPort,
SmtpImplicitTLS, User, Password, AuthMode, RootCAs`. `Password` holds the access token for oauth2 (one secret field); it is trimmed with `strings.TrimSpace` only when
`AuthMode` is oauth2 (inside the XOAUTH2 string); basic keeps the value as today.
`RootCAs` goes into every `tls.Config{ServerName: host, RootCAs: cfg.RootCAs}`. The server builds the config in one
function `emailConfig(env *config.Environment, secret []byte, rootCAs *x509.CertPool) *email.Config` with today's
defaults (IMAP host `Host` else `ImapHost`, port 993; SMTP host `SmtpHost` else `Host`, port 587), `ImapTLS: true`,
`SmtpImplicitTLS: port == 465`, `AuthMode: "oauth2"` iff `env.Auth.Type == "oauth2"`. `Server` gets an unexported
`emailRootCAs *x509.CertPool`, never set outside tests. InsecureSkipVerify is not used anywhere.

**D3 — XOAUTH2 clients.** go-sasl has no XOAUTH2 client (only OAUTHBEARER), so `xoauth2.go` adds
`xoauth2String(user, token) []byte` = `"user=" + user + "\x01auth=Bearer " + token + "\x01\x01"`, an IMAP
`sasl.Client` (`Start` → `"XOAUTH2", ir`; `Next(challenge)` → `[]byte{}`, nil: the server's error challenge is a
base64 JSON and the protocol wants an empty reply, after which the server sends NO), and an SMTP `smtp.Auth`
(`Start` → error unless `server.TLS`, else `"XOAUTH2", ir`; `Next(msg, more)` → `[]byte{}` when `more`, nil when not).
net/smtp `Client.Auth` sends the empty line on 334 and turns the following 535 into a `*textproto.Error`; a nil
response from Next would end the exchange early, hence the non-nil empty slice. go-imap's Authenticate handles both
SASL-IR and the `+` continuation for the initial response. Error texts are the server's status text; the token is
never formatted into an error. `dialImap` with oauth2 refuses to authenticate when the connection is not TLS (`ImapTLS`
false and no STARTTLS offered): error "imap: refusing XOAUTH2 without TLS", no AUTHENTICATE sent (the server always
sets `ImapTLS: true`, so this is a guard only).

**D4 — SMTP transport.** Today `sendTLS` dials implicit TLS on any port (587 then fails the handshake: Gmail and
Outlook use STARTTLS there) and `sendPlain` uses `smtp.SendMail` with opportunistic STARTTLS; the server always sets
`SmtpTLS: true`, so `sendPlain` is unreachable in production. New `dialSMTP(cfg) (*smtp.Client, error)`: with
`SmtpImplicitTLS` a `tls.DialWithDialer` (dialTimeout); else `net.DialTimeout`, `smtp.NewClient`, `Extension("STARTTLS")`
must be true (else error "smtp server does not offer STARTTLS", no AUTH sent) and `StartTLS` must succeed. Then
`Auth` with `smtp.PlainAuth` (basic) or the XOAUTH2 Auth. `Send` = dialSMTP + Mail/Rcpt/Data (as sendTLS today);
`VerifySMTP` = dialSMTP + Auth. The connection gets a deadline of `commandTimeout` so a stalled server cannot hang.
Behaviour change: every SMTP port except 465 (25, 587, 2525) now uses mandatory STARTTLS instead of implicit TLS for
`Send`; `VerifySMTP` already did STARTTLS, so diag changes less. Recorded in HOTMAIL.md Known issues. The existing
`unreachableCfg` in connector_test.go drops `SmtpTLS` (the zero value is STARTTLS mode) and
`TestSendAcceptsValidInputReachesDial` keeps passing on the closed-port dial error.

**D5 — rejection class.** `var ErrAuthRejected = errors.New("email: authentication rejected")`. IMAP: go-imap
v1.2.1 `StatusResp.Err` returns `errors.New(info)` for NO and BAD alike, so the type is gone by the time `Login` or
`Authenticate` returns, and its own closed-connection error (`errClosed`, "imap: connection closed") is untyped too.
The connector therefore decides by connection state, checked right after the call and before `Logout` (which would
close the channel): the error is wrapped
(`fmt.Errorf("imap login: %w: %v", ErrAuthRejected, err)`) only when `imapClient.LoggedOut()` is still open after the
call (non-blocking select: the server answered and kept the connection; `execute` returns errClosed only after that
channel closed) and the error is not a `net.Error`, `io.EOF`, `io.ErrUnexpectedEOF` or `client.ErrLoginDisabled`. A
misclassified BAD or client-side SASL error costs at most one refresh call (accepted). Errors of later commands are
never wrapped. SMTP: the error of `Client.Auth` is wrapped iff it is a `*textproto.Error` with `Code == 535`. Dial, TLS and
STARTTLS errors come before auth and are never wrapped.

**D6 — refresh and retry.** `func (s *Server) withEmail(env *config.Environment, workspace string, call
func(*email.Config) error) error`: resolve `env.SecretRef` with `ResolveWithWorkspaceCheck`, build the config (D2),
run `call`. If the error is ErrAuthRejected, `AuthMode == "oauth2"` and `env.OAuth != nil`: `ok :=
s.refreshOAuthToken(env, workspace)` (unchanged: holds oauthRefreshMu, stores the access token and a rotated refresh
token, flushes the resolver cache); if !ok return `fmt.Errorf("%w: OAuth token refresh failed", err)`; else
re-resolve, rebuild, run `call` once more and return its error. `executeEmailConnector` wraps each action's connector
call (list/search/read/send) in `withEmail`; policy checks stay before the call. Diag replaces its two ad-hoc configs
with `withEmail` around `ListMailboxes` and `VerifySMTP`, keeping its `env.User != ""` guard, its per-protocol
`host != ""` / `smtpHost != ""` guards and result keys. `withEmail` re-resolves the secret on every call, so after
the IMAP call refreshed, the SMTP call uses the new token without a second refresh. Policy checks keep running before
the call; the secret is now resolved after them (harmless order change).

**D7 — search UID.** Add `imap.FetchUid` to the Search fetch items; collect the Fetch goroutine's error and return it
after draining the channel (today it is dropped). Empty mailbox: unchanged early return.

**D8 — tokencheck.** `DetectKind`: `case env.OAuth != nil || env.Auth.Type == "oauth2": return KindOAuth`, so the
IMAP check (LOGIN with the secret) is never run with an access token.

**D9 — kept behaviour.** Two requests rejected at once each refresh after the other (oauthRefreshMu serialises);
Microsoft does not revoke a used refresh token and each refresh stores the newest one, so both succeed at the cost of
an extra token call. A failed store of the rotated refresh token is only logged today (refreshOAuthToken); unchanged
here, documented in HOTMAIL.md Known issues as the case that needs a new device-code run.

**D10 — tests.** See Test plan. Wall time of the five affected packages is measured on master before the change
(baseline 2026-11-14: email 3.3 s, server 51.4 s, tokencheck 3.3 s, config 1.5 s, policy 3.7 s) and after, and both
are reported. Dial-error tests share one closed-port helper (listen on 127.0.0.1:0, close, dial that port).

**D11 — config and docs.** `config.example/config.globex.yaml` site `hotmail` (values in acceptance 9), roles
`dev-bot` and any other role listing `email.*` get the matching `hotmail.*`; `config.example/operations.globex.yaml`
copies the four `email.*` ops with `site: hotmail`. `docs/HOTMAIL.md` sections: Why basic auth fails; Prerequisite:
an Entra directory; Register the app; Get the tokens (device code script, stores via `/admin/secrets` from the
response object without echoing); Config; Operations; Verify (`/admin/diag`); Known issues and blockers. `.ai/INDEX.md`
gets a HOTMAIL.md row; README.md's Hotmail/Outlook row and both Hotmail entries in `.ai/PITFALLS.md` (the "Hotmail/Outlook
OAuth2" bullet and the "Hotmail/Outlook consumer accounts require XOAUTH2" section) say XOAUTH2 is supported and
link the doc. The real mailbox, directory and client id stay in `.ai/LOCAL.md`. The example agent's
`token_sha256: ""` becomes 64 zeros with a comment to replace it with the SHA-256 of the agent token (owner
2026-11-14, chosen over a test-only temp copy): `config.Validate` refuses an empty hash, and no known token hashes to
all zeros, so a deployed but unedited example still admits no agent.

**D12 — hardening, now in acceptance** (owner 2026-11-14). Small, each closing a gap found while designing: XOAUTH2
refuses to send a token without TLS on IMAP (D3 guard) and SMTP (`Start` checks `server.TLS`), both tested; the SMTP
connection gets a `commandTimeout` deadline (D4) and Search returns the Fetch error instead of dropping it (D7), both
without a test by owner decision (a deadline test would wait out the timeout; a mid-stream Fetch failure needs a
broken server for one line). The SMTP guard is unreachable through `dialSMTP` (STARTTLS is mandatory before AUTH), so
its test calls `Start` directly.

## Test plan

- TestEmailConfigFromEnv (internal/server): auth.type oauth2/basic/"" × SMTP port 465/587/0 → AuthMode, SmtpImplicitTLS, ports.
- TestImapXOAuth2Login, TestImapBasicLoginUnchanged, TestImapXOAuth2Rejected (internal/connectors/email): go-imap
  `server.New(memory.New())` on a TLS listener, `EnableAuth("XOAUTH2", fake)`. The server advertises SASL-IR, so
  the IR arrives on the fake's first `Next` call: record it there. On success the fake logs in through the inner
  memory backend and sets `conn.Context().User` and `State` itself (go-imap's server does not); a wrapping backend
  records Login calls, so "no LOGIN arrived" means its counter is 0. The reject fake answers the first `Next` with a
  valid base64 JSON challenge and the second with an error (NO); go-imap's server skips the client's empty line and
  calls `Next` again with the stale IR, so the fake counts calls instead of asserting the empty line (only the SMTP
  fake can). It keeps the connection open until the test's cleanup (a close right after NO races go-imap's
  `execute` select and would read as not rejected).
- TestSmtpStartTLSThenXOAuth2, TestSmtpNoStartTLSRefused, TestSmtpImplicitTLS, TestSmtpBasicPlainUnchanged,
  TestSmtpXOAuth2Rejected: a small line-based fake SMTP server (EHLO, STARTTLS, AUTH, MAIL, RCPT, DATA, QUIT) recording
  each command; reject = `334 <base64 JSON>`, empty line, `535 5.7.3`; TestSmtpXOAuth2Rejected asserts the empty
  line arrived before the 535. After 535 net/smtp sends `*` and QUIT: the fake answers `501` and `221` so nothing
  waits for `commandTimeout`.
- No token in error text: TestImapXOAuth2Rejected and TestSmtpXOAuth2Rejected pass the token with a trailing newline
  and assert `err.Error()` contains neither the token nor its base64 XOAUTH2 string; TestEmailOAuth2RefreshRetry
  asserts the same for `old` and `new` in its failed-refresh case.
- TestDialErrorNotAuthRejected: a listener opened and closed to get a refused 127.0.0.1 port.
- TestImapConnectionClosedNotAuthRejected: a raw TLS listener sends `* OK [CAPABILITY IMAP4rev1 AUTH=XOAUTH2 SASL-IR]`
  (so the client never blocks on CAPABILITY), reads the AUTHENTICATE line, closes.
- TestSearchReturnsUID, TestSearchEmptyMailbox: memory backend user mailbox with three messages, first expunged.
- TestEmailOAuth2RefreshRetry, TestDiagEmailOAuth2 (internal/server): the existing `credStore` (working Set) and the
  SetStore + FlushCache + Cleanup pattern of `newCredHarness` (credential_status_test.go), reused, not a new fake;
  token_url is the httptest server's `http://127.0.0.1` URL (config does not validate it); in-process IMAP/SMTP fakes accepting only token `new`; httptest token endpoint
  counting calls; `s.emailRootCAs` set to the test pool.
- TestDetectKind row (internal/tokencheck): site `email`, `ImapHost` set, `auth.type` oauth2, no oauth block → KindOAuth
  (KindIMAP before the change, so the row fails without it).
- TestExampleHotmailConfig (internal/server): runs `config.Load` on `../../config.example/config.globex.yaml` itself
  (loads once its agent hash is the 64-zero placeholder, D11) and `policy.NewRegistry` on
  `../../config.example/operations.globex.yaml`, and asserts the hotmail site and env fields, the four ops with
  `site: hotmail`, and that every role listing an `email.X` op lists `hotmail.X`.
- TestImapXOAuth2RefusedWithoutTLS (internal/connectors/email): plain (non-TLS) in-process go-imap server without
  STARTTLS, `AllowInsecureAuth` on and the XOAUTH2 fake registered; `ImapTLS: false`, AuthMode oauth2: the error says
  "refusing XOAUTH2 without TLS" and the fake's `Next` was never called.
- TestSmtpXOAuth2AuthRequiresTLS: the XOAUTH2 `smtp.Auth`'s `Start` with `ServerInfo{TLS: false}` returns an error
  (no token in its text); with `TLS: true` it returns `"XOAUTH2"` and the exact XOAUTH2 string.
- Docs: `git grep` for the LOCAL.md address and client id returns nothing (run by the implementer, not a Go test).

## Not changed

`refreshOAuthToken`, the http-rest 401 refresh path, `OAuthRefreshConfig` (no `scope`: the probe showed refresh
without scope returns the IMAP+SMTP scopes), email policy (`filterMailboxes`, `denyResource`, sender filters), op
naming (`<site>.<action>`), `ReadMessage`, live gitignored config (EG0040).
