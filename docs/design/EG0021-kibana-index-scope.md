# EG0021 design: kibana_index_scope is never enforced: kibana.search can query any index

Todo: `todo/EG0021.md` (its acceptance is the contract; this doc says how). Workspace names are placeholders
(`acme`, `globex`; EG0038).

## Decisions in brief

- `kibana_index_scope` becomes `constraintChecked` with its own case in `CheckConstraints` (D1).
- The only accepted `indices` value is the string `$workspace.kibana_indices`; any other value, a missing `indices`
  or an op with no `index` param is refused when the ops file loads (D2).
- An empty `kibana_indices` list leaves the op unscoped: no check at all (owner 2026-11-12; D3).
- The checked value is the `index` param, else the op's declared `index` default (CheckConstraints runs before
  ApplyDefaults); omitted with no default, or not a string, is refused (D4).
- The value is split on commas, each element trimmed; an empty element (also an empty or blank index) is
  refused (D5).
- Each element must pass a character rule: only `A-Z a-z 0-9 . _ - *`, at most 255 bytes, not starting with `-` or
  `_`, not `.`, no `..`. This refuses `_all`, `-x`, `?`, `[`, `]` (owner answers) and also `/ : # & % < >` and
  spaces, which can reach an index outside the glob through Kibana's console proxy path (D6).
- Covered means every name the element can match also matches the glob, case-sensitive, only `*` a wildcard. The
  check is a symbolic match where a glob `*` may consume any element character including an element `*`, and an
  element `*` can only be consumed by a glob `*`. It is sound (never allows an uncovered element) and may refuse
  exotic covered ones; an entry `*` covers everything (D7).
- A refusal is an error from CheckConstraints, so the server answers 403 `rule:"constraint"` before any upstream
  call; no server code changes (D8).
- Deploy step: the acme live `kibana.search` gets the constraint; globex already has it; acme stays unscoped
  until its config sets `kibana_indices` (owner 2026-11-12; D9).

## Acceptance

Restated from `todo/EG0021.md`:

1. `kibana_index_scope` is a checked constraint (`internal/policy/constraint_types.go`) with a case in
   CheckConstraints (`internal/policy/registry.go`); an `indices` value naming an unknown `$workspace.*` variable is
   refused when the ops file loads.
2. The index value is split on commas and each element trimmed; an empty element, an empty or whitespace-only
   index, `_all`, an element starting with `-`, and an element containing `?` `[` or `]` are refused.
3. Each remaining element must be covered, case-sensitively, by some `kibana_indices` glob, where only `*` is a
   wildcard and covered means every index name the element can match also matches the glob (allowed
   `logstash-nth-stg*`: `logstash-nth-stg-2026.11` and `logstash-nth-stg-2026*` covered; `*`, `logstash-*` and
   `logstash-*-2026` not; `Logstash-nth-stg-1` not); the check may refuse a covered pattern it cannot prove but
   never allows an uncovered one. An allowed entry `*` covers everything.
4. When the index param is omitted the constraint checks the op's declared index default (CheckConstraints runs
   before ApplyDefaults); omitted with no default is refused. An empty `kibana_indices` list leaves the op unscoped.
5. Policy tests `TestKibanaIndexScope*` in `internal/policy` have one row per rule above; a server test with an
   httptest upstream shows a refused index gets 403 with JSON rule `constraint` and zero upstream hits.
6. Deploy step (gitignored live files): `operations.acme.yaml` `kibana.search` declares `kibana_index_scope` with
   `indices: $workspace.kibana_indices` (globex already does; acme stays unscoped while `config.acme.yaml` sets no
   `kibana_indices`); after `deploy/update.ps1` a `kibana.search` with `index=secret-*` on globex is refused 403
   before any upstream call, and one with the default index on globex still succeeds.

## Anchors

- `internal/policy/constraint_types.go`: `constraintTypes` (entry `kibana_index_scope`), `checkConstraintTypes`.
- `internal/policy/registry.go`: `OperationRegistry.CheckConstraints`, `OperationRegistry.loadFromFile`,
  `OperationRegistry.ApplyDefaults`, `Operation.Params`, `ParamDef.Default`.
- `internal/policy/jira_key_scope.go`: `parseJiraKeyScope`, `validateJiraKeyScope` (the parse-at-load and
  parse-per-call precedent this follows).
- `internal/config/config.go`: `WorkspacePolicy.KibanaIndices`.
- `internal/server/server.go`: the op handler's `ValidateParams` → `CheckConstraints` (`writeDeny` with rule
  `"constraint"`) → `ApplyDefaults` order; `writeDeny`.
- `internal/server/pathtemplate.go`: `expandPathTemplate` (a `?path={index}` value is `url.QueryEscape`d, which
  Kibana decodes back into a path).
- `config.example/operations.globex.yaml`: `kibana.search` (already declares the constraint).
- `config.example/operations.yaml`: `kibana.search` (no constraint; unchanged).
- `.ai/OPERATIONS.md`: constraint table row `kibana_index_scope`.

## Decisions

### D1. Checked constraint, new file

`constraintTypes["kibana_index_scope"]` becomes `constraintChecked` (the `// Not enforced` comment goes).
New file `internal/policy/kibana_index_scope.go` holds:

```go
// kibanaIndexScopeSource is the only indices value kibana_index_scope accepts.
const kibanaIndexScopeSource = "$workspace.kibana_indices"

func validateKibanaIndexScope(id string, op *Operation) error          // load time (D2)
func checkKibanaIndexScope(op *Operation, constraint map[string]interface{},
    params map[string]interface{}, ws *config.Workspace) error        // request time (D3-D7)
func validKibanaIndexElement(e string) bool                            // D6
func kibanaPatternCovers(elem, glob string) bool                       // D7
```

`CheckConstraints` gets `case "kibana_index_scope": if err := checkKibanaIndexScope(...); err != nil { return err }`.
`loadFromFile` calls `validateKibanaIndexScope` next to `validateJiraKeyScope`. `checkKibanaIndexScope` re-checks
the `indices` value (fails closed for an op built in code that skipped the load check), as `jql_scope` does.

### D2. Load-time checks

For each `kibana_index_scope` constraint of an op, `validateKibanaIndexScope` refuses:
- `indices` missing, not a string, or a string other than `$workspace.kibana_indices` (this covers acceptance 1:
  `$workspace.jira_projects` or `$workspace.nope` is refused; a literal list is not supported, see Not changed);
- an op with no `index` entry in `Params` (the constraint would check nothing), or whose `index` param is not
  `type: string`.

Error text: `operation <id>: kibana_index_scope indices must be $workspace.kibana_indices` and
`operation <id>: kibana_index_scope needs a string index param`. Several `kibana_index_scope` constraints on one
op are each validated and each checked at request time (all must pass), like every other constraint.

### D3. Empty list is unscoped

`allowed := ws.Policy.KibanaIndices`; when `len(allowed) == 0` (or `ws` is nil) the check returns nil before
looking at the param (owner 2026-11-12, as `pg_allowed_tables`). Unscoped means no element rule applies either: an unscoped op (acme today) still forwards any index, `/` and
`_cat` included, to the console proxy. That is the owner's empty-list answer; it ends once the workspace sets
`kibana_indices`.

### D4. Which value is checked

`v, present := params["index"]`; when absent, `v = op.Params["index"].Default` and present is `v != nil`. Not
present → refused (`index is required`). A value that is not a `string` → refused (fail closed; `ValidateParams`
already refuses a non-string for a `type: string` param, this guards an op that declares another type).

### D5. Splitting

`strings.Split(v, ",")`, each element trimmed of ASCII space and tab only (`strings.Trim(e, " \t")`; other whitespace such as NBSP stays
and D6 refuses it). Any empty element after trimming → refused. So `""`,
`"  "`, `"a,,b"`, `"a,"` are refused; `"a, b"` is checked as `a` and `b`. The check only decides; the upstream receives the original, untrimmed value (a
stray space can only make a name that exists nowhere, never a different covered one).

### D6. Element character rule

`validKibanaIndexElement` accepts an element only when all hold:
- length 1..255 bytes (Elasticsearch's index name limit);
- every byte is in `A-Z a-z 0-9 . _ - *`;
- the first byte is not `-` (exclusion) and not `_` (refuses `_all` and every `_api` path such as `_cat`);
- the element is not `.` and contains no `..`.

Why stricter than acceptance 2's list: the live ops send the index as `?path={index}/_search` to Kibana's console
proxy. `expandPathTemplate` query-escapes it, but Kibana decodes it back into the Elasticsearch path, so an
element like `logstash-nth-stg-x/../secret` or `logstash-nth-stg*:secret` (remote-cluster syntax) or
`<date-math>` names a different index while a plain string glob would call it covered. Acceptance 3 forbids
allowing an uncovered element and lets the check refuse what it cannot prove, so these are refused by the
character rule. Every Elasticsearch index name already avoids these characters and a leading `_`, `-` or `+`, so no real index
is lost. A `kibana_indices` glob that itself breaks the rule (for example `_x*` or an empty string) can never
cover a valid element; it is not refused at config load (Not changed). Uppercase stays allowed here so a case mismatch is refused by D7 (acceptance 3), not by D6.

### D7. Covers algorithm

Both strings are first reduced by collapsing runs of `*` into one `*`. `kibanaPatternCovers(e, g)` is a memoised
match over positions `i` in `e` and `j` in `g` (O(len(e)·len(g)), at most 255·len(g)):

```
m(i, j):
  j == len(g):        return i == len(e)
  g[j] == '*':        return m(i, j+1) || (i < len(e) && m(i+1, j))   // glob * eats any element byte, incl. '*'
  otherwise:          return i < len(e) && e[i] != '*' && e[i] == g[j] && m(i+1, j+1)
```

Sound: from a successful match, any replacement of the element's `*`s by strings gives a match of the resulting
name against `g` (the glob `*` that ate an element `*` eats its replacement). So a covered verdict is never wrong.
It may say "not covered" for exotic pairs that are covered over a finite alphabet; acceptance 3 allows that.
Byte comparison is case-sensitive. An element is allowed when some glob in `kibana_indices` covers it; the
error names the first refused element: `kibana_index_scope on <op>: index "<elem>" is not within kibana_indices`.

Worked rows (allowed `logstash-nth-stg*`): `logstash-nth-stg-2026.11` covered (literals, then glob `*` eats the
rest); `logstash-nth-stg-2026*` covered (glob `*` eats `-2026*`); `*` not (glob literal `l` meets element `*`);
`logstash-*` not (`n` meets `*`); `logstash-*-2026` not; `Logstash-nth-stg-1` not (`L` ≠ `l`). Entry `*`: `m`
accepts every element (glob `*` eats everything).

### D8. Server path unchanged

`server.go` already calls `CheckConstraints` before `ApplyDefaults` and before any connector work, and maps its
error to `writeDeny(..., "constraint", err.Error())` (403, audited as deny). Both call sites, `handleDryRun` and
`executeOp`, run `ValidateParams`, `CheckConstraints`, `ApplyDefaults` in that order, so dry runs are refused the
same way. No server code changes; the server test proves the wiring.

### D9. Docs, example and deploy

- `.ai/OPERATIONS.md` row `kibana_index_scope`: describe the check (source `$workspace.kibana_indices`, empty list
  unscoped, comma list, element rule, covers semantics).
- `config.example/operations.globex.yaml` already declares the constraint and has an `index` param with default
  `logstash-*`, covered by its example `kibana_indices`; unchanged.
- Deploy step (gitignored live files, not committed): add to the acme live `kibana.search`
  `constraints: [{type: kibana_index_scope, indices: $workspace.kibana_indices}]`; run `deploy/update.ps1`; then on
  globex `kibana.search` with `index: "secret-*"` → 403 `rule:"constraint"`, and with no index (default
  `logstash-*`) → success (recorded as `test.kind=e2e`). The refusal happens before the upstream call, so it does
  not depend on the Kibana path itself.

## Test plan

Policy tests in new `internal/policy/kibana_index_scope_test.go`, table-driven, workspace `kibana_indices`
`[logstash-nth-stg*]` unless the row says otherwise:

| Acceptance | Test | Rows |
|---|---|---|
| 1 | `TestKibanaIndexScopeLoad` | `indices: $workspace.jira_projects` refused; `$workspace.nope` refused; missing / list refused; op without `index` param refused; `index` param of `type: int` refused; two constraints on one op both validated; valid op loads; `kibana_index_scope` kind is `constraintChecked` |
| 2 | `TestKibanaIndexScopeElements` | `logstash-nth-stg-1, logstash-nth-stg-2` with spaces → ok; `a,,b`; `a,`; empty `""`; blank `"  "`; `_all`; `-x`; `logs?x`; `logs[1]`; plus D6 rows `logstash-nth-stg-x/../secret`, `logstash-nth-stg*:secret`, `..`, 256-byte element refused and 255-byte covered element allowed; `_cat`; NBSP inside an element; leading NBSP (not trimmed, refused) |
| 3 | `TestKibanaIndexScopeCovers` | allowed name `logstash-nth-stg-2026.11`; covered pattern `logstash-nth-stg-2026*`; refused name `secret-1`; wider `*`, `logstash-*`, `logstash-*-2026`; case `Logstash-nth-stg-1`; comma list `logstash-nth-stg-1,secret` refused; entry `*` with list `[*]` allows `anything-*`; with the globex list `[logstash-nth-stg*, logstash-*]`, `logstash-prod-1` is allowed by the second glob only; an empty-string glob covers nothing |
| 3 | `TestKibanaIndexScopeCoversSound` | brute force: every element and glob up to 4 bytes over `{a, b, *}`; whenever `kibanaPatternCovers` is true, every replacement of the element's `*`s by strings of 0 to 2 bytes over `{a, b}` (the empty string included) matches the glob by a reference matcher written independently of `kibanaPatternCovers` (plain recursive glob match on concrete names). It proves soundness only; the Covers table catches an always-false implementation |
| 4 | `TestKibanaIndexScopeDefault` | omitted with default `logstash-nth-stg-1` ok; omitted with default `secret-*` refused; omitted with no default refused; empty `kibana_indices` with `index: _all` ok (unscoped); non-string index refused; omitted with empty `kibana_indices` ok; nil workspace ok (unscoped) |
| 5 | `TestKibanaIndexScopeServer` (`internal/server/kibana_index_scope_test.go`) | op built in code on the fixture's ops: a kibana site pointing at an httptest upstream counting hits (atomic counter, as in `internal/server/jira_fields_test.go`), path `/api/console/proxy?path={index}/_search&method=POST`, workspace `config.WorkspacePolicy{KibanaIndices: [logstash-nth-stg*]}`; `index: "secret-*"` → 403, JSON `error:"denied"`, `rule:"constraint"`, 0 hits; allowed index → 200, 1 hit; dry run with `secret-*` → 403 |
| 6 | e2e (deploy step) | live globex `kibana.search` with `secret-*` → 403 before upstream; default index → success; recorded as `test.kind=e2e` |

Existing tests in `internal/policy/constraint_types_test.go` change with the kind: the "all known types" load
fixture gives `kibana_index_scope` an `indices: $workspace.kibana_indices` line and its op an `index` param (D2
would refuse it otherwise), and `TestCheckConstraintsUnknownTypeFailsClosed` counts 4 no-effect types, not 5.
Run `go test ./internal/policy/ ./internal/server/`.

## Not changed

- No literal `indices` list (only `$workspace.kibana_indices`); no other `$workspace` source.
- The `kibana.search` path and the double-brace check (EG0031, already in `loadFromFile`).
- `config.example/operations.yaml` `kibana.search` (generic example, no constraint, path `/_search`).
- Other Kibana ops and the `cluster` param; `expand_wildcards` and hidden-index behaviour of Elasticsearch.
- `config.acme.yaml` `kibana_indices` (owner's choice when to set it; acme stays unscoped until then).
- No load-time check of the `kibana_indices` globs themselves (a malformed glob just covers nothing).
- The server glob helper that lowercases (unsuitable: case-sensitive matching is required).
