package policy

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/external_gateway/internal/config"
)

// JiraIssueKeyRe matches a Jira issue key; group 1 is the project key. Go's `$`
// is end of text, so a trailing newline does not match. (internal/server/
// jira_worklog.go keeps its own stricter pattern; that is deliberate.)
var JiraIssueKeyRe = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)-[0-9]+$`)

// JiraKeyScopeConstraint scopes Jira issue keys and pinned project keys found at
// dotted paths inside the request params (`- type: jira_key_scope`).
type JiraKeyScopeConstraint struct {
	Projects    []string
	Restrict    bool // false: the project part is not restricted (no workspace jira_projects)
	IssueKeys   []string
	ProjectKeys []string
	Required    []string
	Absent      []string
}

// lookupPath walks "a.b.c" through object members only. A non-object met before
// the last segment is an error, never "absent".
func lookupPath(params map[string]interface{}, path string) (interface{}, bool, error) {
	segs := strings.Split(path, ".")
	var cur interface{} = params
	for i, seg := range segs {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false, fmt.Errorf("path %s: %s is not an object", path, strings.Join(segs[:i], "."))
		}
		next, present := m[seg]
		if !present {
			return nil, false, nil
		}
		cur = next
	}
	return cur, true, nil
}

func (c *JiraKeyScopeConstraint) projectList() string { return strings.Join(c.Projects, ", ") }

// CheckIssueKey validates one value as an in-scope issue key; label names it in errors.
func (c *JiraKeyScopeConstraint) CheckIssueKey(label string, v interface{}) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("path %s must be an issue key string", label)
	}
	if !c.IssueKeyInScope(s) {
		return fmt.Errorf("path %s: %q is not an in-scope issue key (projects: %s)", label, s, c.projectList())
	}
	return nil
}

// IssueKeyInScope reports whether v is a string issue key whose project is in scope.
func (c *JiraKeyScopeConstraint) IssueKeyInScope(v interface{}) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	m := JiraIssueKeyRe.FindStringSubmatch(s)
	return m != nil && (!c.Restrict || containsString(c.Projects, m[1]))
}

func (c *JiraKeyScopeConstraint) Check(op *Operation, params map[string]interface{}, workspace *config.Workspace) error {
	for _, p := range c.Absent {
		_, present, err := lookupPath(params, p)
		if err != nil {
			return err
		}
		if present {
			return fmt.Errorf("path %s is not allowed", p)
		}
	}
	for _, p := range c.Required {
		_, present, err := lookupPath(params, p)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("missing required path %s", p)
		}
	}
	// Pinned holders: fields.project must be {key: ...} and nothing else, so a
	// project cannot be pinned by id or name instead.
	for _, p := range append(append([]string{}, c.IssueKeys...), c.ProjectKeys...) {
		i := strings.LastIndex(p, ".")
		if i < 0 {
			continue
		}
		holder, last := p[:i], p[i+1:]
		hv, present, err := lookupPath(params, holder)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		hm, ok := hv.(map[string]interface{})
		if _, hasKey := hm[last]; !ok || len(hm) != 1 || !hasKey {
			return fmt.Errorf("path %s must be an object with only %q", holder, last)
		}
	}
	for _, p := range c.IssueKeys {
		v, present, err := lookupPath(params, p)
		if err != nil {
			return err
		}
		if present {
			if err := c.CheckIssueKey(p, v); err != nil {
				return err
			}
		}
	}
	for _, p := range c.ProjectKeys {
		v, present, err := lookupPath(params, p)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		s, ok := v.(string)
		if !ok || (c.Restrict && !containsString(c.Projects, s)) {
			return fmt.Errorf("path %s: project %q is not in scope", p, fmt.Sprint(v))
		}
	}
	return nil
}

// parseJiraKeyScope builds the constraint from its config map; any malformed
// field is an error, so a bad op fails closed at check time.
func parseJiraKeyScope(opID string, constraint map[string]interface{}, ws *config.Workspace) (*JiraKeyScopeConstraint, error) {
	c := &JiraKeyScopeConstraint{}
	switch p := constraint["projects"].(type) {
	case string:
		if p != "$workspace.jira_projects" {
			return nil, fmt.Errorf("jira_key_scope on %s: unknown projects source %q (only $workspace.jira_projects)", opID, p)
		}
		if ws != nil {
			c.Projects = ws.Policy.JiraProjects
		}
		c.Restrict = len(c.Projects) > 0
	case []interface{}:
		list, ok := stringList(p)
		if !ok || len(list) == 0 {
			return nil, fmt.Errorf("jira_key_scope on %s: projects must be a non-empty list of project keys", opID)
		}
		for _, s := range list {
			if s == "" {
				return nil, fmt.Errorf("jira_key_scope on %s: projects must be a non-empty list of project keys", opID)
			}
		}
		c.Projects, c.Restrict = list, true
	default:
		return nil, fmt.Errorf("jira_key_scope on %s: projects is required ($workspace.jira_projects or a list)", opID)
	}
	for name, dst := range map[string]*[]string{
		"issue_keys": &c.IssueKeys, "project_keys": &c.ProjectKeys, "required": &c.Required, "absent": &c.Absent,
	} {
		v, present := constraint[name]
		if !present || v == nil {
			continue
		}
		list, ok := stringList(v)
		if !ok {
			return nil, fmt.Errorf("jira_key_scope on %s: %s must be a list of dotted paths", opID, name)
		}
		*dst = list
	}
	return c, nil
}

// JiraKeyScopeFor returns the op's jira_key_scope constraint resolved against ws.
func JiraKeyScopeFor(op *Operation, ws *config.Workspace) (*JiraKeyScopeConstraint, error) {
	for _, constraint := range op.Constraints {
		if t, _ := constraint["type"].(string); t == "jira_key_scope" {
			return parseJiraKeyScope(op.ID, constraint, ws)
		}
	}
	return nil, fmt.Errorf("operation %s has no jira_key_scope constraint", op.ID)
}

// validateJiraKeyScope is the load-time check of every jira_key_scope constraint
// and of the op's precheck.
func validateJiraKeyScope(id string, op *Operation) error {
	hasScope := false
	for _, constraint := range op.Constraints {
		if t, _ := constraint["type"].(string); t != "jira_key_scope" {
			continue
		}
		hasScope = true
		c, err := parseJiraKeyScope(id, constraint, nil)
		if err != nil {
			return fmt.Errorf("operation %s: %w", id, err)
		}
		firsts := map[string]bool{}
		all := map[string][]string{"issue_keys": c.IssueKeys, "project_keys": c.ProjectKeys, "required": c.Required, "absent": c.Absent}
		for name, paths := range all {
			for _, p := range paths {
				segs := strings.Split(p, ".")
				for _, seg := range segs {
					if seg == "" || strings.Contains(seg, "[") {
						return fmt.Errorf("operation %s: jira_key_scope %s path %q has an empty segment or an array index", id, name, p)
					}
				}
				if _, declared := op.Params[segs[0]]; !declared {
					return fmt.Errorf("operation %s: jira_key_scope %s path %q starts with %q, which is not a declared param", id, name, p, segs[0])
				}
				firsts[segs[0]] = true
			}
		}
		for _, p := range c.Required {
			if !containsString(c.IssueKeys, p) && !containsString(c.ProjectKeys, p) {
				return fmt.Errorf("operation %s: jira_key_scope required path %q is not in issue_keys or project_keys", id, p)
			}
		}
		for _, p := range c.Absent {
			if containsString(c.IssueKeys, p) || containsString(c.ProjectKeys, p) || containsString(c.Required, p) {
				return fmt.Errorf("operation %s: jira_key_scope path %q is in absent and in another list", id, p)
			}
		}
		for name := range firsts {
			def := op.Params[name].Default
			if m, ok := def.(map[string]interface{}); def != nil && !(ok && len(m) == 0) {
				return fmt.Errorf("operation %s: jira_key_scope param %s may only default to {} (defaults are applied after constraints)", id, name)
			}
		}
	}
	switch op.Precheck {
	case "":
	case PrecheckJiraIssueLink:
		if op.Connector != "http-rest" || op.SlackFileContent || op.Multipart != nil || op.JiraAttachmentContent {
			return fmt.Errorf("operation %s: precheck jira_issue_link needs an http-rest op without slack_file_content, multipart or jira_attachment_content", id)
		}
		if def, ok := op.Params["id"]; !ok || def.Pattern != "^[0-9]+$" || def.Type != "string" || !def.Required {
			return fmt.Errorf("operation %s: precheck jira_issue_link needs an id param that is a required string with pattern ^[0-9]+$", id)
		}
		if !hasScope {
			return fmt.Errorf("operation %s: precheck jira_issue_link needs a jira_key_scope constraint", id)
		}
	default:
		return fmt.Errorf("operation %s: unknown precheck %q", id, op.Precheck)
	}
	return nil
}
