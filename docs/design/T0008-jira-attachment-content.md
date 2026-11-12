# T0008 design: Jira attachment content (`jira_attachment_content`)

Todo: `todo/T0008.md` (acceptance there is the contract; this note says how).

## Decisions in brief
- Owner 2026-11-12: scope by issue `key` + attachment `id`, checked against the issue's attachment list; types
  images + text + PDF; 1 MB cap; a generic op field, thp wires `corpjira.attachment_content` (§Problem).
- New `policy.Operation` field `JiraAttachmentContent bool` (`yaml:"jira_attachment_content"`), dispatched in
  `handleOp` next to `slack_file_content`, before `executeHttpConnector` (§Dispatch).
- Load-time checks in the registry: http-rest only; not with `slack_file_content`, `multipart`, `body_template`,
  `response.allow_fields` or `precheck`; params `key` and `id` both `required: true` (§Load validation).
- Handler in a new file `internal/server/jira_attachment.go`; two upstream calls through `s.httpClient.Execute`
  with `MaxBodyBytes` 1,048,576 each, not `restClient.do` (it JSON-decodes and has no cap) (§Handler).
- Credential headers come from a new helper `jiraAuthHeader(env, workspace)`: basic (with `user`) or bearer with
  a non-empty `secret_ref`, else an error before any request; `proxy_host` envs are an error too; the secret is
  passed as `Credentials` so it is tainted (§Handler).
- Download URL is built: `base_url + "/secure/attachment/" + id + "/" + url.PathEscape(filename)`; the upstream
  `content` field is never used; redirects are not followed (connector `noRedirect`), so 3xx is an error (§Security).
- Refusals return `*policy.ConstraintDenied` (403 `rule: constraint`); everything else is a plain error, which
  `handleOp` already turns into the status-error reply (§Outcomes).
- Type classes: image (png, jpeg, gif, webp) and pdf -> base64; text (reuse `isTextMimetype`) -> text with invalid
  UTF-8 replaced; mime types lowercased with parameters stripped (§Types).
- Tests drive `ServeHTTP` with a secret store and an httptest mock Jira; a 302 test proves no request leaves for
  another host (§Tests).

## Problem
`corpjira.attachments` returns metadata only; agents fetched `/secure/attachment/<id>/<name>` in the browser.
`executeHttpConnector` (`internal/server/server.go` ~752) passes non-JSON bodies through as text, so binary content
cannot reach the agent intact. `/secure/attachment/{id}/{name}` carries no project, so an id alone could reach any
attachment the site token can read; the issue key is the only scope egw can check (op param pattern and
`jira_key_scope` constraints already run on `key` before dispatch).

## Dispatch
`handleOp` (`server.go` ~568): `if op.Connector == "http-rest" && op.JiraAttachmentContent { ... =
s.executeJiraAttachmentContent(ws, op, params, workspace) }`, placed before the plain http-rest branch. The tail
(ConstraintDenied -> 403, audit, status-error reply, taint redaction) is unchanged.

## Load validation
In the registry's per-op validation (where `slack_file_content` / `multipart` rules live, `internal/policy/registry.go`)
an op with the field set is refused unless: connector `http-rest`; `SlackFileContent` false; `Multipart` nil;
`BodyTemplate` empty; `Response.AllowFields` empty; no `precheck`; `Params["key"]` and `Params["id"]` exist with
`Required: true`. `jira_key_scope.go` ~238 (the `jira_issue_link` precheck guard) adds the field to its exclusion
list, so the message stays true; the precheck itself is refused by the rule above.

## Handler
`executeJiraAttachmentContent(ws, op, params, workspace) (int, []byte, error)`:
1. `key`, `id` strings; `key` must match `policy.JiraIssueKeyRe`, `id` must match `^[0-9]+$`, else error. (The
   op's own param patterns run first in `ValidateParams`; this recheck matters for ops with looser patterns.)
2. Site and env via `resolveEnv`; env `ProxyHost != ""` -> error; `BaseURL` empty or `-` -> error;
   `base = strings.TrimSuffix(env.BaseURL, "/")`.
3. `jiraAuthHeader(env, workspace)` -> `Authorization` value plus a `*connectors.Credentials` (Token for bearer,
   Password for basic) or error: other auth type, empty `secret_ref`, basic with empty `user`, unresolved secret
   (`secrets.ResolveWithWorkspaceCheck`, so a cross-workspace ref fails). Secret is `TrimSpace`d. Both requests pass
   that `Credentials` so the connector taints the secret (connector.go ~112). No request happens before this point.
4. GET `base + "/rest/api/2/issue/" + key + "?fields=attachment"` with `Accept: application/json`, auth,
   `Insecure: env.InsecureSkipVerify`, `MaxBodyBytes: 1<<20`. Transport error (incl. over cap), status != 200
   (incl. 3xx), or bad JSON -> error.
5. Decode `{key, fields: {attachment: [{id, filename, mimeType, size}]}}`. `key != requested key` -> refusal
   ("issue <key> answered as <other>"). First entry with `id == param` (string compare); none -> refusal
   ("attachment <id> is not on issue <key>").
6. Normalise `mimeType` (`normMime`: `strings.ToLower`, cut at `;`, trim); class lookup (§Types); none ->
   refusal naming the type. `size > 1<<20` -> refusal naming size and cap (size is a JSON number; absent or 0
   relies on the body cap). Listed filename empty, `.` or `..` -> error (`url.PathEscape` keeps those as-is).
7. GET `base + "/secure/attachment/" + id + "/" + url.PathEscape(filename)` with auth only (no Accept), same
   `Insecure`, `Credentials` and cap. Transport error (incl. over cap) or status != 200 -> error with the status.
   `headerLookup(resp.Headers, "Content-Type")` present: normalised with `normMime`, its class must equal the
   listed class, else error (a png served as `application/octet-stream` is an error: accepted).
8. Reply `{"attachment": {id, filename, mimeType, size}, "encoding": "base64"|"text", "content": ...}` with status
   200; text content `strings.ToValidUTF8(..., "�")`.

## Types
`attachmentClass(m string) string`: `image/png|jpeg|gif|webp` -> `image`; `application/pdf` -> `pdf`;
`isTextMimetype(m)` (slack_file.go, already excludes text/html and covers json/xml/yaml/x-yaml) -> `text`; else
`""`. image and pdf encode base64 (`base64.StdEncoding`); text returns the string.

## Security
- Credential only ever goes to `base_url`'s host: both URLs are built from `base_url`; the filename is one escaped
  segment (`/`, `?`, `#` are escaped; empty, `.` and `..` are refused), so it cannot change host or path depth;
  `key` matches the anchored `JiraIssueKeyRe`; the connector does not follow redirects.
- The secret is tainted (passed as `Credentials`), so the reply's taint redaction can also rewrite text content
  that happens to contain it; documented in OPERATIONS.md.
- Scope: the attachment must be listed on the requested issue, the issue must answer under the same key, and the key
  passed the op's pattern and constraints. Residual: an attachment listed on an in-scope issue is readable even if
  Jira stores it elsewhere (Jira does not do this).
- Size: 1 MiB on both calls bounds memory; base64 grows the reply to about 1.4 MB.
- Audit logs `key` and `id` (existing `auditParams`), never content.
- No independent security reviewer is configured: specialist security coverage is UNVERIFIED; the owner is asked
  to accept that residual risk at review.

## Outcomes
Refusals (ConstraintDenied, no download): id not listed (also absent/empty list), key mismatch, type not allowed,
listed size over cap. Errors (status-error reply): input pattern, env/auth before any request, issue call failure,
download failure (non-200, 3xx, over cap), Content-Type class mismatch.

## Tests (`internal/server/jira_attachment_test.go`, plus a registry test)
Harness: `newCredHarness` (credential_status_test.go) with a per-test sites block (auth, `user`, `secret_ref`,
`proxy_host`, `base_url` = mock URL), secret via `h.store.Set`; a new helper `opCall(t, h, op, body)` POSTs
`/v1/workspaces/<ws>/op/<op>` with `credAgentTok` and returns status + decoded body. Mock Jira (`httptest.Server`)
records every request's `RequestURI` (escaped form, never `r.URL.Path`) and `Authorization`. The test op file
declares `key` and `id` as required strings with no pattern, so the handler's own rechecks are reachable. Every
case asserts exact values: refusals 403 with `error: denied`, `rule: constraint`, non-empty `detail`; errors the
status-error reply with its text; and the exact list of upstream requests (empty for pre-request cases).
- A1 scope: issue call `RequestURI` exactly `/rest/api/2/issue/SRTHP-1?fields=attachment`; download path for
  filename `a b/ü.png` exactly `/secure/attachment/10/a%20b%2F%C3%BC.png`; a listed `content` pointing at a second
  server, which sees zero requests; ids `101` and `1` listed with param `10` -> refusal; listed `010` with param
  `10` -> refusal; duplicate id 10 -> the first entry's filename is downloaded; base_url with trailing `/` gives
  single-slash paths.
- A2 refusals (zero download requests): id not listed; `fields.attachment` absent; `[]`; key mismatch
  (`SRTHP-2` answered for `SRTHP-1`); `application/zip`; `TEXT/HTML; charset=utf-8`; size 1,048,577. Boundary:
  size 1,048,576 and size absent are accepted.
- A3 types: table test of `attachmentClass` over png, jpeg, gif, webp, pdf, text/plain, text/csv, application/json,
  application/xml, application/yaml, application/x-yaml (accepted) and text/html, application/zip,
  application/octet-stream, empty (refused); end to end: png bytes with 0x00/0xFF -> `encoding: base64`, exact
  bytes; pdf -> base64; text `a\xffb` -> `encoding: text`, content exactly `a�b`; listed `IMAGE/PNG; name=x`
  accepted.
- A4 errors: before any request (zero requests): auth `api-key`; empty `secret_ref`; secret not in store; secret
  ref in another workspace; basic with empty `user`; `proxy_host`; base_url `-`; key `x`; id `1/../2`. After the
  issue call: issue 404; issue 302; issue body 1,048,577 bytes; issue 200 with bad JSON; listed filename `..`
  (no download). After the download: 404; 302 to a second server (zero requests there, reply names 302); body
  1,048,577 bytes (and 1,048,576 accepted); class mismatch with the header sent in canonical case: png listed /
  `text/html` served, pdf listed / `image/png` served, text listed / `image/png` served; Content-Type absent
  accepted.
- A5 reply: whole JSON compared: `attachment` {id, filename, mimeType, size}, `encoding`, `content`.
- A6 headers: bearer -> both calls `Authorization: Bearer s3cr3t` (store value `" s3cr3t\n"`, trimmed); basic ->
  both calls `Basic base64(u:s3cr3t)`.
- Insecure: one test with `httptest.NewTLSServer` and `insecure_skip_verify: true` succeeds; without it, error.
- A7 registry (`internal/policy`), each case fails `NewRegistry` naming the op id, and fails without its rule
  line: connector `postgres`; with `slack_file_content`; with `multipart`; with `body_template`; with
  `response.allow_fields`; with `precheck`; `key` missing; `key` `required: false`; `id` missing; `id`
  `required: false`. A valid op loads. A `jira_issue_link` precheck op with the new field set is refused by the
  new rule (the guard message names the field).

## Docs
`.ai/OPERATIONS.md`: a section for the field and a row for `corpjira.attachment_content` (thp, gitignored).

## Out of scope
Jira Cloud's `/rest/api/2/attachment/content/{id}`, `proxy_host` sites, an example op in `config.example/`,
types beyond the owner's list.
