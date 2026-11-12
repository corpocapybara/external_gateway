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
- Credential headers come from a new helper `jiraAuthHeader(env, workspace)`: basic or bearer with a non-empty
  `secret_ref`, else an error before any request; `proxy_host` envs are an error too (§Handler).
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
1. `key`, `id` strings; `key` must match `policy.JiraIssueKeyRe`, `id` must match `^[0-9]+$`, else error.
2. Site and env via `resolveEnv`; env `ProxyHost != ""` -> error; `BaseURL` empty or `-` -> error.
3. `jiraAuthHeader(env, workspace)` -> `Authorization` value or error (other auth type, empty `secret_ref`,
   unresolved secret). No request happens before this point.
4. GET `base + "/rest/api/2/issue/" + key + "?fields=attachment"` with `Accept: application/json`, auth,
   `Insecure: env.InsecureSkipVerify`, `MaxBodyBytes: 1<<20`. Transport error, status != 200, or bad JSON -> error.
5. Decode `{key, fields: {attachment: [{id, filename, mimeType, size}]}}`. `key != requested key` -> refusal
   ("issue <key> answered as <other>"). First entry with `id == param` (string compare); none -> refusal
   ("attachment <id> is not on issue <key>").
6. Normalise `mimeType` (`strings.ToLower`, cut at `;`, trim); class lookup (§Types); none -> refusal naming the
   type. `size > 1<<20` -> refusal naming size and cap.
7. GET the built URL (§Security) with auth only (no Accept), same `Insecure` and cap. Transport error (including
   the cap) or status != 200 -> error with the status. `Content-Type` header present and its class differs from the
   listed class -> error.
8. Reply `{"attachment": {id, filename, mimeType, size}, "encoding": "base64"|"text", "content": ...}` with status
   200; text content `strings.ToValidUTF8(..., "�")`.

## Types
`attachmentClass(m string) string`: `image/png|jpeg|gif|webp` -> `image`; `application/pdf` -> `pdf`;
`isTextMimetype(m)` (slack_file.go, already excludes text/html and covers json/xml/yaml/x-yaml) -> `text`; else
`""`. image and pdf encode base64 (`base64.StdEncoding`); text returns the string.

## Security
- Credential only ever goes to `base_url`'s host: both URLs are built from `base_url`; the filename is one escaped
  segment, so `/`, `..`, `?`, `#` cannot change host or path depth; the connector does not follow redirects.
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
Harness: config + ops YAML written to a temp dir as `newCredHarness` does (credential_status_test.go), secret in
the `credStore`, mock Jira `httptest.Server` counting requests per path.
- png bytes (incl. 0x00 and 0xFF) -> base64 round-trips exactly through `ServeHTTP`; pdf base64; text with invalid
  UTF-8 -> U+FFFD; listed `IMAGE/PNG; name=x` accepted; listed png with download `image/jpeg` accepted; missing
  Content-Type accepted.
- Exact upstream paths incl. filename `a b/ü.png` -> `/secure/attachment/10/a%20b%2F%C3%BC.png`.
- Refusals, each 403 `constraint` and zero download requests: id not listed, absent list, key mismatch,
  `application/zip`, size 1,048,577.
- Errors, each status-error reply: auth `api-key` and empty `secret_ref` and `proxy_host` (zero requests), issue
  404, download 404, download 302 to a second server (second server sees zero requests), body 1,048,577 bytes,
  listed png with download `text/html`.
- Headers: Bearer and Basic on both calls.
- Registry: each refused combination in §Load validation fails `NewRegistry` with the op id in the message; a valid
  op loads.

## Docs
`.ai/OPERATIONS.md`: a section for the field and a row for `corpjira.attachment_content` (thp, gitignored).

## Out of scope
Jira Cloud's `/rest/api/2/attachment/content/{id}`, `proxy_host` sites, an example op in `config.example/`,
types beyond the owner's list.
