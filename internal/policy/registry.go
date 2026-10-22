package policy

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/external_gateway/internal/config"
	"gopkg.in/yaml.v3"
)

type OperationRegistry struct {
	Operations map[string]*Operation
	TunnelChecker TunnelChecker
}

type TunnelChecker interface {
	CheckTunnel(ctx context.Context, tunnelName string) (bool, string, error)
}

type Operation struct {
	ID          string
	Connector   string
	Site        string
	Method      string
	Path        string
	Params      map[string]ParamDef
	BodyTemplate string     `yaml:"body_template"`
	Headers     map[string]string        `yaml:"headers"`
	Multipart   *MultipartSpec           `yaml:"multipart"`
	Constraints []map[string]interface{} `yaml:"constraints"`
	Response    ResponsePolicy
}

// MultipartSpec sends the request as multipart/form-data with one file part,
// built from a filename param and a base64 content param.
type MultipartSpec struct {
	Field         string `yaml:"field"`
	FilenameParam string `yaml:"filename_param"`
	ContentParam  string `yaml:"content_param"`
}

type ParamDef struct {
	Type     string
	Required bool
	From     string
	Values   []string
	Max      int    `yaml:"max"`
	MaxLen   int    `yaml:"maxlen"`
	Pattern  string `yaml:"pattern"`
	Keys     []string `yaml:"keys"`
	Default  interface{}
}

type Constraint interface {
	Check(op *Operation, params map[string]interface{}, workspace *config.Workspace) error
}

type ResponsePolicy struct {
	AllowFields []string `yaml:"allow_fields"`
}

type JQLScopeConstraint struct {
	Projects []string
}

func (c *JQLScopeConstraint) Check(op *Operation, params map[string]interface{}, workspace *config.Workspace) error {
	jql, ok := params["jql"].(string)
	if !ok || strings.TrimSpace(jql) == "" {
		return fmt.Errorf("jql parameter is required")
	}
	projects := c.Projects
	if projects == nil {
		projects = workspace.Policy.JiraProjects
	}
	if len(projects) == 0 {
		// No configured project scope: nothing to enforce.
		return nil
	}
	// Enforce scope by constraining the query to the allowed projects rather than
	// trusting a substring match — which `project = OTHER OR summary ~ "ALLOWED"`
	// trivially defeated. Wrapping preserves the original query's precedence:
	//   (<jql>) AND project in (A,B)
	// Project keys are simple identifiers, so they are emitted unquoted (quotes
	// would be JSON-escaped when jql is placed into a request body or query).
	// A trailing ORDER BY is not part of the filter: it is split off first and
	// re-appended after the scope, since JQL rejects it inside parentheses.
	filter, order, err := splitJQLOrderBy(jql)
	if err != nil {
		return err
	}
	scoped := fmt.Sprintf("project in (%s)", strings.Join(projects, ","))
	if filter != "" {
		scoped = fmt.Sprintf("(%s) AND %s", filter, scoped)
	}
	if order != "" {
		scoped += " " + order
	}
	params["jql"] = scoped
	return nil
}

var (
	orderByStart  = regexp.MustCompile(`(?i)^order\s+by\b`)
	orderField    = `(?:"[^"]*"|'[^']*'|cf\[\d+\]|[A-Za-z_][A-Za-z0-9_.]*)(?:\s+(?:asc|desc))?`
	orderByClause = regexp.MustCompile(`(?i)^order\s+by\s+` + orderField + `(?:\s*,\s*` + orderField + `)*$`)
)

// splitJQLOrderBy separates the filter from a top-level trailing ORDER BY clause.
// Quotes and parentheses must balance, so a stray ")" in the filter cannot close
// the scope wrapper early (`x) OR (project = OTHER`), and the ORDER BY clause may
// only list sort fields, so it cannot smuggle in a predicate.
func splitJQLOrderBy(jql string) (filter, order string, err error) {
	var quote byte
	depth := 0
	orderAt := -1
	for i := 0; i < len(jql); i++ {
		c := jql[i]
		if quote != 0 {
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return "", "", fmt.Errorf("jql has an unbalanced parenthesis")
			}
		case 'o', 'O':
			if depth == 0 && orderAt < 0 && (i == 0 || !isJQLWordByte(jql[i-1])) && orderByStart.MatchString(jql[i:]) {
				orderAt = i
			}
		}
	}
	if quote != 0 || depth != 0 {
		return "", "", fmt.Errorf("jql has an unbalanced quote or parenthesis")
	}
	if orderAt < 0 {
		return strings.TrimSpace(jql), "", nil
	}
	order = strings.TrimSpace(jql[orderAt:])
	if !orderByClause.MatchString(order) {
		return "", "", fmt.Errorf("jql ORDER BY clause must list only sort fields, each optionally ASC or DESC")
	}
	return strings.TrimSpace(jql[:orderAt]), order, nil
}

func isJQLWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

type PostgresReadOnlyConstraint struct{}

func (c *PostgresReadOnlyConstraint) Check(op *Operation, params map[string]interface{}, workspace *config.Workspace) error {
	return nil
}

type AllowedTablesConstraint struct {
	Tables []string
}

func (c *AllowedTablesConstraint) Check(op *Operation, params map[string]interface{}, workspace *config.Workspace) error {
	return nil
}

type TunnelConstraint struct {
	TunnelName string
}

func (c *TunnelConstraint) Check(op *Operation, params map[string]interface{}, workspace *config.Workspace) error {
	return nil
}

type OperationsConfig struct {
	Operations map[string]*Operation `yaml:"operations"`
}

func NewRegistry(opsConfigPath string) (*OperationRegistry, error) {
	reg := &OperationRegistry{
		Operations: make(map[string]*Operation),
	}

	if opsConfigPath != "" {
		if err := reg.loadFromFile(opsConfigPath); err != nil {
			return nil, fmt.Errorf("loading operations from %s: %w", opsConfigPath, err)
		}
	}

	if len(reg.Operations) == 0 {
		reg.registerBuiltIn()
	}

	return reg, nil
}

func (r *OperationRegistry) loadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading operations file: %w", err)
	}

	var cfg OperationsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parsing operations file: %w", err)
	}

	for id, op := range cfg.Operations {
		for name, def := range op.Params {
			if def.Pattern == "" {
				continue
			}
			if _, err := regexp.Compile(def.Pattern); err != nil {
				return fmt.Errorf("operation %s param %s: invalid pattern: %w", id, name, err)
			}
		}
		if mp := op.Multipart; mp != nil {
			_, hasName := op.Params[mp.FilenameParam]
			_, hasContent := op.Params[mp.ContentParam]
			if mp.Field == "" || !hasName || !hasContent || op.BodyTemplate != "" {
				return fmt.Errorf("operation %s: multipart needs field plus declared filename_param and content_param, and no body_template", id)
			}
		}
		op.ID = id
		r.Operations[id] = op
	}

	return nil
}

func (r *OperationRegistry) registerBuiltIn() {
	r.Operations["jira.search"] = &Operation{
		ID:        "jira.search",
		Connector: "http-rest",
		Site:      "jira",
		Method:    "POST",
		Path:      "/rest/api/3/search/jql",
		Params: map[string]ParamDef{
			"jql":        {Type: "string", Required: true},
			"maxResults": {Type: "int", Max: 100, Default: 50},
		},
		Constraints: nil,
		Response: ResponsePolicy{
			AllowFields: []string{"issues[].key", "issues[].fields.summary", "issues[].fields.status"},
		},
	}

	r.Operations["jira.create"] = &Operation{
		ID:        "jira.create",
		Connector: "http-rest",
		Site:      "jira",
		Method:    "POST",
		Path:      "/rest/api/2/issue",
		Params: map[string]ParamDef{
			"project":     {Type: "enum", Required: true},
			"type":        {Type: "enum", Values: []string{"Bug", "Task", "Story"}, Required: true},
			"summary":     {Type: "string", MaxLen: 255, Required: true},
			"description": {Type: "string", MaxLen: 8000},
		},
		BodyTemplate: `{"fields":{"project":{"key":"{{project}}"},"issuetype":{"name":"{{type}}"},"summary":"{{summary}}","description":"{{description}}"}}`,
		Constraints:  nil,
	}

	r.Operations["jira.comment"] = &Operation{
		ID:        "jira.comment",
		Connector: "http-rest",
		Site:      "jira",
		Method:    "POST",
		Path:      "/rest/api/3/issue/{issueKey}/comment",
		Params: map[string]ParamDef{
			"issueKey": {Type: "string", Required: true},
			"body":     {Type: "string", MaxLen: 8000, Required: true},
		},
		Constraints: nil,
	}

	r.Operations["jira.status"] = &Operation{
		ID:        "jira.status",
		Connector: "http-rest",
		Site:      "jira",
		Method:    "GET",
		Path:      "/rest/api/3/issue/{issueKey}/transitions",
		Params: map[string]ParamDef{
			"issueKey": {Type: "string", Required: true},
		},
		Constraints: nil,
	}

	r.Operations["pg.query"] = &Operation{
		ID:        "pg.query",
		Connector: "postgres",
		Site:      "postgres",
		Params: map[string]ParamDef{
			"env": {Type: "string", Required: true},
			"db":  {Type: "string", Required: true},
			"sql": {Type: "string", Required: true},
		},
		Constraints: nil,
	}
}

func (r *OperationRegistry) Get(id string) (*Operation, bool) {
	op, ok := r.Operations[id]
	return op, ok
}

func (r *OperationRegistry) ApplyDefaults(op *Operation, params map[string]interface{}) {
	if params == nil {
		return
	}
	for name, def := range op.Params {
		if _, exists := params[name]; !exists && def.Default != nil {
			params[name] = def.Default
		}
	}
}

func (r *OperationRegistry) ValidateParams(op *Operation, params map[string]interface{}, workspace *config.Workspace) error {
	for name, def := range op.Params {
		val, exists := params[name]
		if def.Required && !exists {
			return fmt.Errorf("missing required parameter: %s", name)
		}
		if !exists {
			continue
		}
		switch def.Type {
		case "string":
			s, ok := val.(string)
			if !ok {
				return fmt.Errorf("parameter %s must be a string", name)
			}
			if def.MaxLen > 0 && len(s) > def.MaxLen {
				return fmt.Errorf("parameter %s exceeds max length %d", name, def.MaxLen)
			}
			if def.Pattern != "" {
				re, err := regexp.Compile(def.Pattern)
				if err != nil {
					return fmt.Errorf("parameter %s has an invalid pattern: %w", name, err)
				}
				if !re.MatchString(s) {
					return fmt.Errorf("parameter %s must match %s", name, def.Pattern)
				}
			}
		case "object":
			m, ok := val.(map[string]interface{})
			if !ok {
				return fmt.Errorf("parameter %s must be a JSON object", name)
			}
			if def.Keys != nil {
				for k := range m {
					if !containsString(def.Keys, k) {
						return fmt.Errorf("parameter %s has key %q, allowed keys are %v", name, k, def.Keys)
					}
				}
			}
		case "int":
			f, ok := val.(float64)
			if !ok {
				return fmt.Errorf("parameter %s must be an integer", name)
			}
			if def.Max > 0 && f > float64(def.Max) {
				return fmt.Errorf("parameter %s exceeds max %d", name, def.Max)
			}
		case "bool":
			if _, ok := val.(bool); !ok {
				return fmt.Errorf("parameter %s must be a boolean", name)
			}
		case "enum":
			s, ok := val.(string)
			if !ok {
				return fmt.Errorf("parameter %s must be a string", name)
			}
			if strings.HasPrefix(def.From, "$workspace.") {
				field := strings.TrimPrefix(def.From, "$workspace.")
				allowed, known := workspace.Policy.List(field)
				// Enforce membership only when the allow-list is configured. An
				// empty/unset list means the operator has not restricted this
				// value, not "deny everything".
				if known && len(allowed) > 0 {
					found := false
					for _, p := range allowed {
						if p == s {
							found = true
							break
						}
					}
					if !found {
						return fmt.Errorf("parameter %s=%q is not in the workspace's allowed %s", name, s, field)
					}
				}
			} else if def.Values != nil {
				found := false
				for _, v := range def.Values {
					if v == s {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("parameter %s must be one of %v", name, def.Values)
				}
			}
		}
	}

	// Reject parameters the operation does not declare. Undeclared params were
	// otherwise forwarded verbatim as upstream query parameters (e.g. ?sudo=root
	// or ?per_page=100000) — an arbitrary injection/amplification vector.
	// `env`/`cluster` select the environment and are always permitted.
	for name := range params {
		if name == "env" || name == "cluster" {
			continue
		}
		if _, declared := op.Params[name]; !declared {
			return fmt.Errorf("unknown parameter: %s", name)
		}
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (r *OperationRegistry) CheckConstraints(op *Operation, params map[string]interface{}, workspace *config.Workspace) error {
	for _, constraint := range op.Constraints {
		constraintType, _ := constraint["type"].(string)
		switch constraintType {
		case "jql_scope":
			projectsVal := constraint["projects"]
			var projects []string
			if projectsStr, ok := projectsVal.(string); ok && strings.HasPrefix(projectsStr, "$workspace.") {
				switch projectsStr {
				case "$workspace.jira_projects":
					projects = workspace.Policy.JiraProjects
				case "$workspace.bitbucket_repos":
					projects = workspace.Policy.BitbucketRepos
				case "$workspace.pg_allowed_tables":
					projects = workspace.Policy.PgAllowedTables
				}
			} else if list, ok := projectsVal.([]interface{}); ok {
				// A literal list pins the scope in the operation itself (a second Jira
				// with its own projects); an empty one denies rather than unscopes.
				projects = []string{}
				for _, p := range list {
					if s, ok := p.(string); ok && s != "" {
						projects = append(projects, s)
					}
				}
				if len(projects) == 0 {
					return fmt.Errorf("jql_scope on %s lists no projects", op.ID)
				}
			}
			c := &JQLScopeConstraint{Projects: projects}
			if err := c.Check(op, params, workspace); err != nil {
				return err
			}
		case "readonly":
			c := &PostgresReadOnlyConstraint{}
			if err := c.Check(op, params, workspace); err != nil {
				return err
			}
		case "allowed_tables":
			c := &AllowedTablesConstraint{}
			if err := c.Check(op, params, workspace); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *OperationRegistry) CheckTunnel(tunnelName string) error {
	if r.TunnelChecker == nil {
		return fmt.Errorf("tunnel checker not configured")
	}
	if tunnelName == "" {
		return nil
	}
	connected, details, err := r.TunnelChecker.CheckTunnel(context.Background(), tunnelName)
	if err != nil {
		return fmt.Errorf("tunnel check failed: %w", err)
	}
	if !connected {
		return fmt.Errorf("tunnel %s is down: %s", tunnelName, details)
	}
	return nil
}
