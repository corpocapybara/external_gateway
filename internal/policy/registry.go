package policy

import (
	"context"
	"fmt"
	"os"
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
	Constraints []map[string]interface{} `yaml:"constraints"`
	Response    ResponsePolicy
}

type ParamDef struct {
	Type     string
	Required bool
	From     string
	Values   []string
	Max      int    `yaml:"max"`
	MaxLen   int    `yaml:"maxlen"`
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
	if !ok || jql == "" {
		return fmt.Errorf("jql parameter is required")
	}
	upperJQL := strings.ToUpper(jql)
	for _, project := range workspace.Policy.JiraProjects {
		if strings.Contains(upperJQL, strings.ToUpper(project)) {
			return nil
		}
	}
	return fmt.Errorf("JQL does not scope to allowed projects %v", workspace.Policy.JiraProjects)
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
		case "int":
			_, ok := val.(float64)
			if !ok {
				return fmt.Errorf("parameter %s must be an integer", name)
			}
		case "enum":
			s, ok := val.(string)
			if !ok {
				return fmt.Errorf("parameter %s must be a string", name)
			}
			if def.From == "$workspace.jira_projects" {
				found := false
				for _, p := range workspace.Policy.JiraProjects {
					if p == s {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("parameter %s must be one of %v", name, workspace.Policy.JiraProjects)
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
	return nil
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
