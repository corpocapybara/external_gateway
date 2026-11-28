# EG0077 design: add ability to connect to edu.gdansk.pl

## Decisions in brief
- D1: new connector `uonet`: package `internal/connectors/uonet`, executor `executeUonetConnector` in `internal/server/uonet.go`, dispatch `pickEnv: true, shape: true` (§D1).
- D2: action = op id after `uonet.`; a fixed table of the nine read endpoints is the allowlist; no YAML path reaches upstream (§D2).
- D3: env = `base_url` (portal + symbol), `user`, `secret_ref`; allowed domain = base host minus its first label; module hosts `uonetplus-uczen.<domain>`, `uonetplus-wiadomosciplus.<domain>`; student instance read from `Start.mvc` (§D3).
- D4: login follower on one client + cookie jar: manual redirects, https, domain or subdomain only, no IP, hop cap 30; Username/Password POSTed only to a same-host form with a `Password` input; hidden wa/wresult forms auto-posted; login page again = `ErrLoginRefused` (§D4).
- D5: `formsession.Sessions` becomes generic `sessioncache.Cache[T]` (refused sentinel per cache, `Cookie` renamed `Get`); uonet caches `*uonet.Session`; cleared where form-session's is (§D5).
- D6: data calls never follow redirects; 3xx or non-JSON = expired: invalidate, re-login, retry once (§D6).
- D7: child cookies are added per request and dropped by the jar; `student` (IdUczen) picks the child, default first IdDziennik != 0 (§D7).
- D8: `date` YYYY-MM-DD, default today Europe/Warsaw, sent as its Monday (§D8).
- D9: results `{"data"}` / `{"mailboxes"|"messages"|"message"}`; success:false, bad JSON, HTML, 5xx are errors; 8 MiB cap (§D9).
- D10: e2e deploys the branch build from the worktree with all workspace files copied in (§Test plan).

## Acceptance
Restated from the todo (frozen at `designed`):
1. Registration: `uonet` in `knownConnectors` and `connectorDispatches` (`pickEnv`, `shape`); `TestLoadKnownConnectors` and `TestConnectorDispatchShapeFlags` updated; new `TestUonetDispatch` fails on master.
2. Login chain (`TestLoginChain`): 302s, Username/Password form POST in the body only to the login page, two or more auto-post forms, module LoginEndpoint hops, cookies per host, no password in errors, logs or responses.
3. Login guards (`TestLoginGuards`): other host, `http://`, IP literal refused before sending; hop cap.
4. Refused login backoff 60 s, expiry, per key; 5xx/timeout/broken hop without backoff (`TestLoginRefusedBackoff`).
5. Session cache (`TestSessions` with `-race`) and server clears on reload and secret write (`TestUonetSessionsCleared`).
6. Expired session: one relogin and one retry, both modules (`TestRelogin`).
7. Nine ops in `config.example/operations.yaml`; upstream requests per op, Monday, school year, default period (`TestOpRequests`).
8. Student selection (`TestStudentSelection`).
9. Param validation from the ops file (`TestOpsFileParams`).
10. Endpoint allowlist (`TestEndpointAllowlist`).
11. Upstream failures as errors (`TestUpstreamFailures`).
12. Docs: `docs/UONET.md`, `.ai/INDEX.md`, commented site in `config.example/config.yaml`.
13. <ws> workspace files (untracked): site, agent role grants, ops; diff in the closing report.
14. e2e on the branch build, the <ws> port.

## Anchors
- `internal/policy/connectors.go`: `knownConnectors`, `KnownConnectors`.
- `internal/policy/connector_load_test.go`: `TestLoadKnownConnectors`.
- `internal/server/dispatch.go`: `connectorDispatches`, `connectorDispatch`, `dispatchConnector`.
- `internal/server/dispatch_shape_test.go`: `TestConnectorDispatchShapeFlags` (`selfShaping`).
- `internal/server/server.go`: `Server` (fields `formLogin`, `formSessions`), `NewServer`'s field setup, `resolveEnv`, `parseSecretRef`, `formSessionCookie`, the `s.formSessions.Clear()` calls in the secrets handler (set and delete) and the reload handler, `executeOp` (error body `"status":"error"`).
- `internal/connectors/formsession/sessions.go`: `Sessions`, `NewSessions`, `Key`, `Cookie`, `Invalidate`, `Clear`, `SetWaitHook`, `SetNow`, `loginBackoff`.
- `internal/connectors/formsession/connector.go`: `ErrLoginRefused`.
- `internal/secrets`: `ResolveWithWorkspaceCheck`, `GetTaintRegistry`.
- `internal/config/config.go`: `Environment` (`BaseURL`, `User`, `SecretRef`, `InsecureSkipVerify`).
- `config.example/operations.yaml`, `config.example/config.yaml` (commented form-session `codimd` site as model).
- `deploy/update.ps1` (`$BuildDir`), `deploy/workspaces.ps1` (`Get-EgwWorkspaces`).

## Decisions

**D1 Own connector and executor.** `http-rest`/`form-session` run one templated request; UONET+ needs a multi-hop login, two module hosts, per-request child cookies and envelope unwrapping. `executeHttpConnector` would gain a third special case in a ~300-line function; rejected. `internal/connectors/uonet` holds `Client` (login, `Do`), `Session`, the endpoint table and request builders, with no server imports. `internal/server/uonet.go` holds `executeUonetConnector(ws, op, params, workspace)`: resolveEnv, check `user`/`secret_ref`, get the session from the cache (login on a miss: resolve the password with `secrets.ResolveWithWorkspaceCheck`, call `uonet.Login`), run the action, return `(200, body, nil)` or an error. Dispatch entry `"uonet": {pickEnv: true, shape: true, run: (*Server).executeUonetConnector}`; `selfShaping` does not list it; `TestLoadKnownConnectors` pins `uonet` last.

**D2 Fixed endpoint table.** `action := strings.TrimPrefix(op.ID, "uonet.")`; the table maps action to module (student/messages), method and a builder that turns validated params into a path and body:

| action | module | request |
|---|---|---|
| students | student | `POST UczenDziennik.mvc/Get` `{}` (also cached at login) |
| grades | student | `POST Oceny.mvc/Get` `{"okres":<period>}` |
| attendance | student | `POST Frekwencja.mvc/Get` `{"idTypWpisuFrekwencji":-1,"data":<monday>}` |
| timetable | student | `POST PlanZajec.mvc/Get` `{"data":<monday>}` |
| exams | student | `POST Sprawdziany.mvc/Get` `{"data":<monday>,"rokSzkolny":<year>}` |
| homework | student | `POST Homework.mvc/Get` `{"date":<monday>,"schoolYear":<year>,"statusFilter":-1}` |
| mailboxes | messages | `GET api/Skrzynki` |
| messages | messages | `GET api/Odebrane?idLastWiadomosc=<last_id>&pageSize=<page_size>` (defaults 0, 50) |
| message | messages | `GET api/WiadomoscSzczegoly?apiGlobalKey=<api_global_key>` |

An action not in the table is an error before any request. Path parts come only from the table; param values enter only JSON bodies (encoded by `json.Marshal`) or query values (built with `url.Values`), after the connector re-checks them (integers parsed with `strconv`, the key against a GUID regexp, the date with `time.Parse`), so `/`, `..`, `%2e`, `?` or a host in a value is refused. The op YAML has `connector: uonet`, `site: uonet`, params and `response`, and `method: GET`/`path: /` only if the loader needs them (ignored).

**D3 Config and hosts.** One env per site: `base_url: https://uonetplus.edu.gdansk.pl/gdansk`, `user: <login>`, `secret_ref: <ws>/uonet`. `Login` refuses a base URL that is not https, whose host has fewer than three labels, or whose first label is not `uonetplus`. domain = host without its first label; symbol = the first path segment. Student module base = `https://uonetplus-uczen.<domain>/<symbol>/<instance>` where instance comes from the first `href` on the portal `Start.mvc` page matching `https://uonetplus-uczen.<domain>/<symbol>/<instance>/LoginEndpoint.aspx` (none: login error "no student module link"). Messages base = `https://uonetplus-wiadomosciplus.<domain>/<symbol>`.

**D4 Login follower.** `Login(ctx, baseURL, user, password, opts) (*Session, error)`: a fresh `cookiejar.Jar` (wrapped, D7), client with `CheckRedirect` returning `http.ErrUseLastResponse` (the follower walks redirects itself), timeout 30 s per request. `follow(startURL)` loops up to the shared hop cap (30 requests across the whole login): check the URL (https, hostname is domain or ends with `.`+domain, `net.ParseIP(host) == nil`; else error "login hop to <host> refused" before sending); send; on 3xx resolve `Location` against the request URL and continue; on 200 parse the HTML with a small tokenizer in the package (`forms.go`: `<form action method>` and its `<input name value type>` tags, attribute values HTML-unescaped with `html.UnescapeString`; `golang.org/x/net` is not a dependency and is not added); a form with an input named `Password` → if the password was already posted, return `ErrLoginRefused` (wrapped "uonet login refused: the login page came back"); else POST `Username`, `Password` plus the form's other hidden inputs, form-encoded, to the form action resolved against the page URL, and only if that action's host equals the page's host; a form whose hidden inputs include `wa` and `wresult` → POST its inputs to its action (same checks); otherwise stop. Any status >= 400 is an error with the status and host only. Errors never include bodies or form values. Sequence: `follow(<base>/LoginEndpoint.aspx)` must end on the portal host; GET `<base>/Start.mvc` (D3); `follow(<student base>/LoginEndpoint.aspx)` must end on the student host; `follow(<messages base>/LoginEndpoint.aspx)` must end on the messages host; a module hop that shows the Password form is an error (the portal session was not accepted), not a refusal. Then `POST UczenDziennik.mvc/Get` fills `Session.Students`. The password is tainted with `secrets.GetTaintRegistry().Taint` by the server before the call.

**D5 Generic session cache.** Move `Sessions` and its entry from `internal/connectors/formsession/sessions.go` to `internal/connectors/sessioncache` as `Cache[T comparable]` with `New[T](refused error) *Cache[T]`; `Get(key, login func() (T, error)) (T, error)` (was `Cookie`), `Invalidate`, `Clear`, `SetWaitHook`, `SetNow`, `Key`; the backoff branch tests `errors.Is(err, c.refused)`; the zero T means "none". formsession keeps the rest of its API: `type Sessions = sessioncache.Cache[string]`, `func NewSessions() *Sessions { return sessioncache.New[string](ErrLoginRefused) }`, `func Key(...) string { return sessioncache.Key(...) }`; its one rename is `Cookie` → `Get`, and its callers (`formSessionCookie` in `server.go`, `sessions_test.go`) switch to `Get` in the same change. `loginBackoff` moves with the cache (still 60 s). Rejected: a copy of the cache in uonet (two copies of a concurrency-sensitive file drift); storing a session id string and a side map (two maps to keep in step). `Server` gains `uonetSessions *sessioncache.Cache[*uonet.Session]` (refused = `uonet.ErrLoginRefused`), cleared next to every `s.formSessions.Clear()`. formsession's existing tests run unchanged apart from the rename and guard the move.

**D6 Expiry and retry.** Data calls use the session's client with the same no-follow `CheckRedirect`. A 3xx, or a 200 whose body does not start (after whitespace) with `{` or `[`, is `errSessionExpired`. The executor then calls `Invalidate(key, sess)`, gets a session again (fresh login) and retries once; a second `errSessionExpired` is an error "uonet session expired again after re-login". A 5xx is an error with no retry.

**D7 Children and cookies.** The jar wrapper's `SetCookies` drops cookies named `idBiezacyUczen`, `idBiezacyDziennik`, `idBiezacyDziennikPrzedszkole`, `idBiezacyDziennikWychowankowie`, `biezacyRokSzkolny`; a student data call adds them with `req.AddCookie` from the chosen child (`IdUczen`, `IdDziennik`, `IdPrzedszkoleDziennik`, `IdWychowankowieDziennik`, `DziennikRokSzkolny`). Choice: `student` param (IdUczen) → that child, unknown → error "student <id> not found (one of <ids>)"; no param → first child with `IdDziennik != 0`, none → error "no child with a school diary (students: <ids>)". `uonet.students` returns, per child, `IdUczen`, `UczenImie`, `UczenNazwisko`, `IdDziennik`, `IdPrzedszkoleDziennik`, `IdWychowankowieDziennik`, `DziennikRokSzkolny`, `Okresy` (Id, NumerOkresu, DataOd, DataDo), from a fresh `UczenDziennik.mvc/Get`, not the cache.

**D8 Dates and periods.** `date` param `YYYY-MM-DD` (pattern in the op), default today in `Europe/Warsaw` (fallback UTC if the zone is missing); sent as the Monday of its ISO week, formatted `2006-01-02T00:00:00`. Year = the child's `DziennikRokSzkolny`. `period` param = an `Okresy[].Id`; default the period whose `DataOd <= today <= DataDo`, else the last one; a child with no periods → error.

**D9 Results and failures.** Student: decode `{"data","success","feedback"}`; `success != true` → error "uonet <action>: <feedback.Message>" or "uonet <action>: request failed" when empty; else `{"data": <data>}`. Messages: decode the JSON value into `{"mailboxes"|"messages"|"message": v}`. Malformed JSON → error. Responses are read through `io.LimitReader(8 MiB + 1)`; larger → error. Example ops carry `response.allow_fields` only where the docs pin fields (students); others pass through.

## Test plan
- 1 → `TestLoadKnownConnectors`, `TestConnectorDispatchShapeFlags` (updated), `TestUonetDispatch` (internal/server/uonet_test.go: a fake portal/student server via httptest TLS with `InsecureSkipVerify` in the env, a `uonet.students` op through `dispatchConnector`).
- 2 → `TestLoginChain` (internal/connectors/uonet/login_test.go): one TLS httptest server routing by Host header to fake portal, adfs, login, student and messages hosts under `*.edu.test` (the test injects the dialer so all names reach the server and the domain is `edu.test`); asserts body-only POST, cookies per host, a captured logger/error/response free of the password.
- 3 → `TestLoginGuards` (table: other domain, `http://`, `127.0.0.1`, endless redirect, endless auto-post forms).
- 4 → `TestLoginRefusedBackoff` (injected clock via `SetNow`; rows: refused, backoff expires, other key, 500, timeout, broken module hop).
- 5 → `TestSessions` (internal/connectors/sessioncache/cache_test.go, `-race`: single login, isolation, invalidate keeps newer, clear drops backoff, concurrent callers during backoff do not log in); `TestUonetSessionsCleared` (internal/server/uonet_test.go: reload and secret write clear the uonet cache). The moved formsession tests keep guarding form-session.
- 6 → `TestRelogin` (3xx and HTML answers, retry once, second expiry errors, both modules).
- 7 → `TestOpRequests` (table over nine actions; a Wednesday date sent as Monday; default period).
- 8 → `TestStudentSelection`.
- 9 → `TestOpsFileParams` (loads `config.example/operations.yaml` via the policy registry; bad values refused).
- 10 → `TestEndpointAllowlist`.
- 11 → `TestUpstreamFailures`.
- 12 → read-through of the doc files (review lanes).
- 13 → diff of `config.<ws>.yaml`/`operations.<ws>.yaml` in the closing report.
- 14 → e2e: owner sets `egw/<ws>/uonet` (desktop app or `/admin/secrets`); copy `config.*.yaml` and `operations.*.yaml` from the main checkout into `.worktrees/EG0077`; run `.worktrees/EG0077/deploy/update.ps1` elevated (one UAC prompt); call the five ops on the <ws> port with its agent token; one attempt, no retry on a refused login.

## Not changed
- `executeHttpConnector` and the form-session login (`formsession.Connector`): untouched apart from the cache rename (D5).
- No write op (send message, justify absence): a later todo.
- The `X-V-*` headers and `VParam` token are not sent (live calls succeeded without them); if Vulcan starts requiring them, that is a follow-up.
- Audit, metrics and `/capabilities`: unchanged; uonet ops appear there like any op.
