# EG0039 design: Connector to hotmail account (IMAP/SMTP XOAUTH2)

## Decisions in brief

- D1: `auth.type: oauth2` on an email site selects XOAUTH2; anything else stays basic. No new config key (§Decisions D1).
- D2: `email.Config` gets `AuthMode`, `SmtpImplicitTLS` (replaces `SmtpTLS`) and `RootCAs`; one `emailConfig(env, secret, rootCAs)` in the server builds it for ops and diag (§D2).
- D3: XOAUTH2 lives in `internal/connectors/email/xoauth2.go`: a go-sasl `sasl.Client` for IMAP and a `net/smtp.Auth` for SMTP; on a server challenge both answer an empty response; the SMTP Auth refuses to start without TLS (§D3).
- D4: SMTP: one `dialSMTP` for Send and VerifySMTP; implicit TLS iff `SmtpImplicitTLS`, else plain + mandatory STARTTLS before AUTH. `sendPlain` and the opportunistic path go (§D4).
- D5: `email.ErrAuthRejected` wraps a Login/Authenticate error only while `LoggedOut()` is still open and it is no net/EOF/LOGINDISABLED error, and an SMTP `*textproto.Error` 535 from AUTH (§D5).
- D6: server helper `withEmail(env, workspace, call)` runs the call; on ErrAuthRejected with oauth2 + `oauth` block it calls the unchanged `refreshOAuthToken` once, re-resolves the secret and retries once; a failed refresh returns `%w: OAuth token refresh failed` (§D6).
- D7: `Search` fetches `imap.FetchUid` and returns a Fetch error instead of dropping it (§D7).
- D8: `tokencheck.DetectKind` returns `KindOAuth` also for `auth.type: oauth2` (§D8).
- D9: Concurrent refreshes and a failed rotated-refresh-token store keep today's behaviour; documented, not changed (§D9).
- D10: Tests run in-process on 127.0.0.1 with one generated ECDSA cert (SAN 127.0.0.1) per test package; no sleeps, closed port for dial errors (§Test plan).
- D11: Example config and ops in `config.example/*.globex.yaml`; How To in `docs/HOTMAIL.md`; no real address or client id committed (§D11).

## Acceptance (restated, from todo/EG0039.md)

1. Config seams: `AuthMode`, `SmtpImplicitTLS`, `RootCAs`; server mapping; nil RootCAs in production; TestEmailConfigFromEnv.
2. IMAP XOAUTH2 exact string (trimmed token); basic keeps LOGIN; TestImapXOAuth2Login, TestImapBasicLoginUnchanged.
3. SMTP implicit TLS vs mandatory STARTTLS; AUTH XOAUTH2 / PLAIN; TestSmtpStartTLSThenXOAuth2, TestSmtpNoStartTLSRefused, TestSmtpImplicitTLS, TestSmtpBasicPlainUnchanged.
4. Auth rejection at wire level incl. the challenge handshake; TestImapXOAuth2Rejected, TestSmtpXOAuth2Rejected, TestDialErrorNotAuthRejected, TestImapConnectionClosedNotAuthRejected; no token in error text.
5. One helper for ops and diag; refresh once and retry once; failed refresh error; TestEmailOAuth2RefreshRetry.
6. Search UIDs; TestSearchReturnsUID, TestSearchEmptyMailbox.
7. Diag imap_login and smtp_login via the helper; TestDiagEmailOAuth2.
8. tokencheck DetectKind for oauth2 without oauth block.
9. Example config and ops; TestExampleHotmailConfig.
10. docs/HOTMAIL.md, INDEX, README, PITFALLS; no real values committed.
11. Affected packages' tests pass, wall time reported, fast in-process tests.

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
- go-imap v1.2.1 status.go: type ErrStatusResp
- Go stdlib net/smtp/smtp.go: func (c *Client) Auth

## Decisions

**D1 — selector.** The email site's existing `auth.type` already exists in `config.AuthConfig` and is `basic` for
Gmail. `oauth2` is a new value, read only by the email path and tokencheck; no YAML key is added. The access token
lives in `secret_ref` (as for http-rest bearer sites) and the refresh settings in the existing `oauth` block
(`OAuthRefreshConfig`), so `/admin/status` already lists the refresh and client secrets (admin_status.go `add` of
`oauth-refresh`). Alternatives: a new `email_auth:` key (rejected: duplicates `auth.type`), or Graph Mail via http-rest
(rejected by the owner, 2026-11-14: email policy would not apply).

**D2 — config and seams.** `email.Config` fields after the change: `ImapHost, ImapPort, ImapTLS, SmtpHost, SmtpPort,
SmtpImplicitTLS, User, Password, AuthMode, RootCAs`. `Password` holds the access token for oauth2 (one secret field,
trimmed with `strings.TrimSpace` before use in either mode for oauth2 only; basic keeps the value as today).
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
never formatted into an error.

**D4 — SMTP transport.** Today `sendTLS` dials implicit TLS on any port (587 then fails the handshake: Gmail and
Outlook use STARTTLS there) and `sendPlain` uses `smtp.SendMail` with opportunistic STARTTLS; the server always sets
`SmtpTLS: true`, so `sendPlain` is unreachable in production. New `dialSMTP(cfg) (*smtp.Client, error)`: with
`SmtpImplicitTLS` a `tls.DialWithDialer` (dialTimeout); else `net.DialTimeout`, `smtp.NewClient`, `Extension("STARTTLS")`
must be true (else error "smtp server does not offer STARTTLS", no AUTH sent) and `StartTLS` must succeed. Then
`Auth` with `smtp.PlainAuth` (basic) or the XOAUTH2 Auth. `Send` = dialSMTP + Mail/Rcpt/Data (as sendTLS today);
`VerifySMTP` = dialSMTP + Auth. The connection gets a deadline of `commandTimeout` so a stalled server cannot hang.

**D5 — rejection class.** `var ErrAuthRejected = errors.New("email: authentication rejected")`. IMAP: go-imap
v1.2.1 `StatusResp.Err` returns `errors.New(info)` for NO and BAD alike, so the type is gone by the time `Login` or
`Authenticate` returns, and its own closed-connection error (`errClosed`, "imap: connection closed") is untyped too.
The connector therefore decides by connection state: the error is wrapped
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
with `withEmail` around `ListMailboxes` and `VerifySMTP`, keeping its `env.User != ""` guard and result keys.

**D7 — search UID.** Add `imap.FetchUid` to the Search fetch items; collect the Fetch goroutine's error and return it
after draining the channel (today it is dropped). Empty mailbox: unchanged early return.

**D8 — tokencheck.** `DetectKind`: `case env.OAuth != nil || env.Auth.Type == "oauth2": return KindOAuth`, so the
IMAP check (LOGIN with the secret) is never run with an access token.

**D9 — kept behaviour.** Two requests rejected at once each refresh after the other (oauthRefreshMu serialises);
Microsoft does not revoke a used refresh token and each refresh stores the newest one, so both succeed at the cost of
an extra token call. A failed store of the rotated refresh token is only logged today (refreshOAuthToken); unchanged
here, documented in HOTMAIL.md Known issues as the case that needs a new device-code run.

**D10 — tests.** See Test plan.

**D11 — config and docs.** `config.example/config.globex.yaml` site `hotmail` (values in acceptance 9), roles
`dev-bot` and any other role listing `email.*` get the matching `hotmail.*`; `config.example/operations.globex.yaml`
copies the four `email.*` ops with `site: hotmail`. `docs/HOTMAIL.md` sections: Why basic auth fails; Prerequisite:
an Entra directory; Register the app; Get the tokens (device code script, stores via `/admin/secrets` from the
response object without echoing); Config; Operations; Verify (`/admin/diag`); Known issues and blockers. The real
mailbox, directory and client id stay in `.ai/LOCAL.md`.

## Test plan

- TestEmailConfigFromEnv (internal/server): auth.type oauth2/basic/"" × SMTP port 465/587/0 → AuthMode, SmtpImplicitTLS, ports.
- TestImapXOAuth2Login, TestImapBasicLoginUnchanged, TestImapXOAuth2Rejected (internal/connectors/email): go-imap
  `server.New(memory.New())` on a TLS listener, `EnableAuth("XOAUTH2", fake)` recording the decoded IR; a wrapping
  backend records Login calls; the reject fake sends a valid base64 JSON challenge, then NO, and keeps the connection
  open until the test's cleanup (a close right after NO races go-imap's `execute` select and would read as not
  rejected).
- TestSmtpStartTLSThenXOAuth2, TestSmtpNoStartTLSRefused, TestSmtpImplicitTLS, TestSmtpBasicPlainUnchanged,
  TestSmtpXOAuth2Rejected: a small line-based fake SMTP server (EHLO, STARTTLS, AUTH, MAIL, RCPT, DATA, QUIT) recording
  each command; reject = `334 <base64 JSON>`, empty line, `535 5.7.3`; TestSmtpXOAuth2Rejected asserts the empty
  line arrived before the 535.
- TestDialErrorNotAuthRejected: a listener opened and closed to get a refused 127.0.0.1 port.
- TestImapConnectionClosedNotAuthRejected: a raw TLS listener sends greeting and capabilities, reads AUTHENTICATE, closes.
- TestSearchReturnsUID, TestSearchEmptyMailbox: memory backend user mailbox with three messages, first expunged.
- TestEmailOAuth2RefreshRetry, TestDiagEmailOAuth2 (internal/server): recording fake store (Set works), FlushCache
  after SetStore, restored in Cleanup; in-process IMAP/SMTP fakes accepting only token `new`; httptest token endpoint
  counting calls; `s.emailRootCAs` set to the test pool.
- TokenCheck DetectKind row (internal/tokencheck).
- TestExampleHotmailConfig (internal/server): loads both example files.
- Docs: `git grep` for the LOCAL.md address and client id returns nothing (run by the implementer, not a Go test).

## Not changed

`refreshOAuthToken`, the http-rest 401 refresh path, `OAuthRefreshConfig` (no `scope`: the probe showed refresh
without scope returns the IMAP+SMTP scopes), email policy (`filterMailboxes`, `denyResource`, sender filters), op
naming (`<site>.<action>`), `ReadMessage`, live gitignored config (EG0040).
