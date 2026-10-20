//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"gopkg.in/yaml.v3"
)

type fileChange struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type wsEdit struct {
	a      *app
	ws     string
	key    string
	cfg    *yamlFile
	ops    *yamlFile
	parsed *config.Config
}

func (a *app) openEdit(ws string) (*wsEdit, error) {
	if !validName.MatchString(ws) {
		return nil, fmt.Errorf("invalid workspace")
	}
	cfg, err := openYAML(a.repoConfigPath(ws))
	if err != nil {
		return nil, err
	}
	ops, err := openYAML(a.repoOpsPath(ws))
	if err != nil {
		return nil, err
	}
	parsed, err := loadConfig(a.repoConfigPath(ws))
	if err != nil {
		return nil, err
	}
	return &wsEdit{a: a, ws: ws, key: workspaceKey(ws, parsed), cfg: cfg, ops: ops, parsed: parsed}, nil
}

func (e *wsEdit) workspaceNode() *yaml.Node {
	return mapEnsure(mapEnsure(e.cfg.root(), "workspaces"), e.key)
}

func (e *wsEdit) sitesNode() *yaml.Node { return mapEnsure(e.workspaceNode(), "sites") }

func (e *wsEdit) opsNode() *yaml.Node { return mapEnsure(e.ops.root(), "operations") }

func (e *wsEdit) rolesNode() *yaml.Node { return mapEnsure(e.cfg.root(), "roles") }

// changes renders both files and returns only those that differ.
func (e *wsEdit) changes() ([]fileChange, error) {
	var out []fileChange
	for _, f := range []*yamlFile{e.cfg, e.ops} {
		after, err := f.patched()
		if err != nil {
			return nil, err
		}
		if after != f.original {
			out = append(out, fileChange{Path: f.path, Before: f.original, After: after})
		}
	}
	return out, nil
}

// commit validates the new files with the gateway's own loaders, backs up the old ones and writes.
func (e *wsEdit) commit(apply bool) ([]fileChange, error) {
	changes, err := e.changes()
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("nothing to change")
	}
	tmp, err := os.MkdirTemp("", "egw-desktop-validate")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	cfgTmp, opsTmp := filepath.Join(tmp, "config.yaml"), filepath.Join(tmp, "operations.yaml")
	cfgText, opsText := e.cfg.original, e.ops.original
	for _, c := range changes {
		if c.Path == e.cfg.path {
			cfgText = c.After
		} else {
			opsText = c.After
		}
	}
	os.WriteFile(cfgTmp, []byte(cfgText), 0600)
	os.WriteFile(opsTmp, []byte(opsText), 0600)
	if _, err := config.Load(cfgTmp); err != nil {
		return nil, fmt.Errorf("the edited config would not load: %w", err)
	}
	if _, err := policy.NewRegistry(opsTmp); err != nil {
		return nil, fmt.Errorf("the edited operations would not load: %w", err)
	}
	if !apply {
		return changes, nil
	}
	backupDir := filepath.Join(e.a.dataDir, "backups", time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, err
	}
	for _, c := range changes {
		if err := os.WriteFile(filepath.Join(backupDir, filepath.Base(c.Path)), []byte(c.Before), 0600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(c.Path, []byte(c.After), 0644); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

type opInfo struct {
	ID        string   `json:"id"`
	Site      string   `json:"site"`
	Connector string   `json:"connector"`
	Method    string   `json:"method"`
	Path      string   `json:"path"`
	Params    []string `json:"params"`
	Roles     []string `json:"roles,omitempty"`
	Source    string   `json:"source,omitempty"`
	YAML      string   `json:"yaml"`
	Write     bool     `json:"write"`
}

func describeOp(id string, n *yaml.Node) opInfo {
	get := func(k string) string {
		if v := mapGet(n, k); v != nil {
			return v.Value
		}
		return ""
	}
	info := opInfo{ID: id, Site: get("site"), Connector: get("connector"), Method: get("method"), Path: get("path")}
	if info.Site == "" {
		info.Site = strings.SplitN(id, ".", 2)[0]
	}
	info.Params = mapKeys(mapGet(n, "params"))
	m := strings.ToUpper(info.Method)
	info.Write = m == "POST" || m == "PUT" || m == "PATCH" || m == "DELETE"
	if m == "POST" && (strings.Contains(id, "search") || strings.Contains(id, "query")) {
		info.Write = false
	}
	text, _ := renderNode(mapping(id, n))
	info.YAML = text
	return info
}

// catalog lists operation definitions from every operations*.yaml in the checkout and config.example.
func (a *app) catalog() map[string]opInfo {
	out := map[string]opInfo{}
	var files []string
	for _, g := range []string{filepath.Join(a.buildDir, "operations*.yaml"), filepath.Join(a.buildDir, "config.example", "operations*.yaml")} {
		m, _ := filepath.Glob(g)
		files = append(files, m...)
	}
	for _, f := range files {
		y, err := openYAML(f)
		if err != nil {
			continue
		}
		ops := mapGet(y.root(), "operations")
		for i := 0; ops != nil && i+1 < len(ops.Content); i += 2 {
			id := ops.Content[i].Value
			if _, seen := out[id]; seen {
				continue
			}
			info := describeOp(id, ops.Content[i+1])
			info.Source = filepath.Base(f)
			out[id] = info
		}
	}
	return out
}

func (a *app) catalogNode(id, source string) (*yaml.Node, error) {
	path := filepath.Join(a.buildDir, source)
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join(a.buildDir, "config.example", source)
	}
	if strings.ContainsAny(source, `\/`) || !strings.HasPrefix(source, "operations") {
		return nil, fmt.Errorf("invalid catalog source")
	}
	y, err := openYAML(path)
	if err != nil {
		return nil, err
	}
	n := mapGet(mapGet(y.root(), "operations"), id)
	if n == nil {
		return nil, fmt.Errorf("%s not found in %s", id, source)
	}
	return n, nil
}

type capabilities struct {
	Sites     []string          `json:"sites"`
	Roles     []string          `json:"roles"`
	Tunnels   []string          `json:"tunnels"`
	Ops       []opInfo          `json:"ops"`
	Catalog   []opInfo          `json:"catalog"`
	Templates []serviceTemplate `json:"templates"`
	Envs      map[string][]envSummary `json:"envs"`
	Disabled  map[string]bool         `json:"disabled"`
}

type envSummary struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Auth    string `json:"auth"`
	User    string `json:"user,omitempty"`
	Secret  string `json:"secret,omitempty"`
	Tunnel  string `json:"tunnel,omitempty"`
}

func (a *app) capabilities(ws string) (*capabilities, error) {
	e, err := a.openEdit(ws)
	if err != nil {
		return nil, err
	}
	c := &capabilities{Envs: map[string][]envSummary{}, Templates: serviceTemplates, Disabled: map[string]bool{}}
	w := e.parsed.Workspaces[e.key]
	c.Sites = sortedKeys(w.Sites)
	c.Roles = sortedKeys(e.parsed.Roles)
	c.Tunnels = sortedKeys(e.parsed.Tunnels)
	for _, site := range c.Sites {
		c.Disabled[site] = w.Sites[site].Disabled
		for _, envName := range sortedKeys(w.Sites[site].Environments) {
			env := w.Sites[site].Environments[envName]
			base := env.BaseURL
			if base == "" {
				base = env.Host
			}
			_, secret := splitRef(env.SecretRef)
			c.Envs[site] = append(c.Envs[site], envSummary{Name: envName, BaseURL: base, Auth: env.Auth.Type, User: env.User, Secret: secret, Tunnel: env.RequiresTunnel})
		}
	}
	granted := map[string][]string{}
	for _, r := range c.Roles {
		for _, op := range e.parsed.Roles[r].Operations {
			granted[op] = append(granted[op], r)
		}
	}
	have := map[string]bool{}
	ops := e.opsNode()
	for i := 0; i+1 < len(ops.Content); i += 2 {
		id := ops.Content[i].Value
		info := describeOp(id, ops.Content[i+1])
		info.Roles = granted[id]
		c.Ops = append(c.Ops, info)
		have[id] = true
	}
	for _, info := range a.catalog() {
		if !have[info.ID] {
			c.Catalog = append(c.Catalog, info)
		}
	}
	sort.Slice(c.Catalog, func(i, j int) bool { return c.Catalog[i].ID < c.Catalog[j].ID })
	return c, nil
}

func splitRef(ref string) (string, string) {
	rest := strings.TrimPrefix(ref, "secret://")
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i], rest[i+1:]
	}
	return "", rest
}

// setGrants makes exactly the given roles grant the operation.
func (e *wsEdit) setGrants(id string, roles []string) error {
	want := map[string]bool{}
	for _, r := range roles {
		want[r] = true
	}
	rolesNode := e.rolesNode()
	for _, r := range mapKeys(rolesNode) {
		ops := seqEnsure(mapGet(rolesNode, r), "operations")
		if want[r] && !seqHas(ops, id) {
			ops.Content = append(ops.Content, scalar(id))
		}
		if !want[r] {
			seqRemove(ops, id)
		}
		delete(want, r)
	}
	for r := range want {
		return fmt.Errorf("unknown role %s", r)
	}
	return nil
}

type customOp struct {
	Method       string        `json:"method"`
	Path         string        `json:"path"`
	Connector    string        `json:"connector"`
	BodyTemplate string        `json:"body_template"`
	AllowFields  []string      `json:"allow_fields"`
	Params       []customParam `json:"params"`
}

type customParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Default  string `json:"default"`
}

func buildCustomOp(site string, c customOp) (*yaml.Node, error) {
	method := strings.ToUpper(strings.TrimSpace(c.Method))
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return nil, fmt.Errorf("method must be GET, POST, PUT, PATCH or DELETE")
	}
	if !strings.HasPrefix(c.Path, "/") {
		return nil, fmt.Errorf("path must start with /")
	}
	connector := c.Connector
	if connector == "" {
		connector = "http-rest"
	}
	n := mapping("connector", connector, "site", site, "method", method, "path", c.Path)
	if len(c.Params) > 0 {
		params := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, p := range c.Params {
			if !regexpParam.MatchString(p.Name) {
				return nil, fmt.Errorf("invalid parameter name %q", p.Name)
			}
			typ := p.Type
			if typ == "" {
				typ = "string"
			}
			def := mapping("type", typ)
			if p.Required {
				mapSet(def, "required", typedScalar("true", "!!bool"))
			}
			if p.Default != "" {
				tag := "!!str"
				if typ == "int" {
					tag = "!!int"
				}
				mapSet(def, "default", typedScalar(p.Default, tag))
			}
			mapSet(params, p.Name, def)
		}
		mapSet(n, "params", params)
	}
	if strings.TrimSpace(c.BodyTemplate) != "" {
		mapSet(n, "body_template", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(c.BodyTemplate) + "\n", Style: yaml.LiteralStyle})
	}
	if len(c.AllowFields) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, f := range c.AllowFields {
			if f = strings.TrimSpace(f); f != "" {
				seq.Content = append(seq.Content, scalar(f))
			}
		}
		if len(seq.Content) > 0 {
			mapSet(n, "response", mapping("allow_fields", seq))
		}
	}
	return n, nil
}

var regexpParam = identRe

type addCapabilityRequest struct {
	WS     string    `json:"ws"`
	ID     string    `json:"id"`
	Source string    `json:"source"`
	Custom *customOp `json:"custom"`
	Roles  []string  `json:"roles"`
	Apply  bool      `json:"apply"`
}

// addOperation copies a catalog definition (renamed to the target site) or builds a custom one.
func (e *wsEdit) addOperation(id, source, targetSite string, custom *customOp) error {
	parts := strings.SplitN(id, ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return fmt.Errorf("operation id must look like <site>.<name>")
	}
	site := parts[0]
	if targetSite != "" {
		site = targetSite
	}
	if _, ok := e.parsed.Workspaces[e.key].Sites[site]; !ok {
		return fmt.Errorf("workspace %s has no site %q - add the service first", e.ws, site)
	}
	newID := site + "." + parts[1]
	if !regexpOpName.MatchString(parts[1]) {
		return fmt.Errorf("invalid operation name %q", parts[1])
	}
	if mapGet(e.opsNode(), newID) != nil {
		return fmt.Errorf("%s already exists", newID)
	}
	var node *yaml.Node
	var err error
	if custom != nil {
		node, err = buildCustomOp(site, *custom)
	} else {
		var src *yaml.Node
		src, err = e.a.catalogNode(id, source)
		if err == nil {
			copyNode := *src
			node = deepCopy(&copyNode)
			if s := mapGet(node, "site"); s != nil {
				s.Value = site
			}
		}
	}
	if err != nil {
		return err
	}
	mapSet(e.opsNode(), newID, node)
	return nil
}

var regexpOpName = identRe

func deepCopy(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = nil
	for _, ch := range n.Content {
		c.Content = append(c.Content, deepCopy(ch))
	}
	return &c
}

func (e *wsEdit) removeOperation(id string) error {
	if !mapDelete(e.opsNode(), id) {
		return fmt.Errorf("%s not found", id)
	}
	return e.setGrants(id, nil)
}

type serviceTemplate struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Site         string   `json:"site"`
	BaseURL      string   `json:"base_url"`
	AuthType     string   `json:"auth_type"`
	AuthHeader   string   `json:"auth_header,omitempty"`
	NeedsUser    bool     `json:"needs_user"`
	UserHint     string   `json:"user_hint,omitempty"`
	Secret       string   `json:"secret"`
	CookieSecret string   `json:"cookie_secret,omitempty"`
	Postgres     bool     `json:"postgres,omitempty"`
	Hint         string   `json:"hint"`
	CatalogSites []string `json:"catalog_sites"`
}

var serviceTemplates = []serviceTemplate{
	{ID: "gitlab", Label: "GitLab", Site: "gitlab", BaseURL: "https://gitlab.com/api/v4", AuthType: "bearer", Secret: "gitlab", CatalogSites: []string{"gitlab"},
		Hint: "Personal access token with read_api (+ read_repository)."},
	{ID: "github", Label: "GitHub", Site: "github", BaseURL: "https://api.github.com", AuthType: "bearer", Secret: "github", CatalogSites: []string{"github"},
		Hint: "Classic or fine-grained personal access token."},
	{ID: "jira", Label: "Jira Cloud", Site: "jira", BaseURL: "https://<your-site>.atlassian.net", AuthType: "basic", NeedsUser: true, UserHint: "your Atlassian account email", Secret: "jira", CatalogSites: []string{"jira"},
		Hint: "Atlassian API token; add `jira_projects` to the workspace policy for jql_scope."},
	{ID: "confluence", Label: "Confluence Cloud", Site: "confluence", BaseURL: "https://<your-site>.atlassian.net/wiki", AuthType: "basic", NeedsUser: true, UserHint: "your Atlassian account email", Secret: "jira", CatalogSites: []string{"confluence"},
		Hint: "Usually shares the Jira API token secret."},
	{ID: "jenkins", Label: "Jenkins", Site: "jenkins", BaseURL: "https://jenkins.example.com", AuthType: "basic", NeedsUser: true, UserHint: "your Jenkins user id", Secret: "jenkins", CatalogSites: []string{"jenkins"},
		Hint: "Jenkins user API token."},
	{ID: "bitbucket", Label: "Bitbucket Cloud", Site: "bitbucket", BaseURL: "https://api.bitbucket.org", AuthType: "basic", NeedsUser: true, UserHint: "Atlassian email (API token) or username", Secret: "bitbucket", CatalogSites: []string{"bitbucket"},
		Hint: "Atlassian API token with Bitbucket scopes."},
	{ID: "slack", Label: "Slack (session token)", Site: "slack", BaseURL: "https://slack.com/api", AuthType: "slack-session", Secret: "slack-xoxc", CookieSecret: "slack-xoxd", CatalogSites: []string{"slack"},
		Hint: "Browser session: xoxc token + d cookie."},
	{ID: "clockify", Label: "Clockify", Site: "clockify", BaseURL: "https://api.clockify.me/api/v1", AuthType: "api-key", AuthHeader: "X-Api-Key", Secret: "clockify", CatalogSites: []string{"clockify"},
		Hint: "Clockify API key."},
	{ID: "kibana", Label: "Kibana", Site: "kibana", BaseURL: "https://kibana.example.com", AuthType: "basic", NeedsUser: true, Secret: "kibana", CatalogSites: []string{"kibana"},
		Hint: "Kibana user password."},
	{ID: "postgres", Label: "PostgreSQL", Site: "postgres", Postgres: true, NeedsUser: true, Secret: "pg", CatalogSites: []string{"postgres"},
		Hint: "Read-only database user; host, port and one database."},
	{ID: "rest-bearer", Label: "Other REST API (bearer token)", Site: "", BaseURL: "https://api.example.com", AuthType: "bearer", Hint: "Any API that takes Authorization: Bearer <token>."},
	{ID: "rest-basic", Label: "Other REST API (user + password/token)", Site: "", BaseURL: "https://api.example.com", AuthType: "basic", NeedsUser: true, Hint: "Any API with HTTP basic auth."},
	{ID: "rest-apikey", Label: "Other REST API (API key header)", Site: "", BaseURL: "https://api.example.com", AuthType: "api-key", AuthHeader: "X-Api-Key", Hint: "Any API that takes the key in a header."},
}

type addServiceRequest struct {
	WS           string   `json:"ws"`
	Template     string   `json:"template"`
	Site         string   `json:"site"`
	Env          string   `json:"env"`
	BaseURL      string   `json:"base_url"`
	AuthHeader   string   `json:"auth_header"`
	User         string   `json:"user"`
	Secret       string   `json:"secret"`
	CookieSecret string   `json:"cookie_secret"`
	Tunnel       string   `json:"tunnel"`
	Insecure     bool     `json:"insecure"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	Database     string   `json:"database"`
	Ops          []string          `json:"ops"`
	Roles        []string          `json:"roles"`
	SiteVars     map[string]string `json:"site_vars"`
	Apply        bool              `json:"apply"`
}

var siteVarKeys = map[string]bool{"org": true, "group": true, "workspace": true, "workspace_id": true, "user_id": true}

var siteVarRe = regexp.MustCompile(`\{site\.([a-z_]+)\}`)

func (e *wsEdit) addService(req addServiceRequest) error {
	var tpl *serviceTemplate
	for i := range serviceTemplates {
		if serviceTemplates[i].ID == req.Template {
			tpl = &serviceTemplates[i]
		}
	}
	if tpl == nil {
		return fmt.Errorf("unknown template")
	}
	if !identRe.MatchString(req.Site) || !identRe.MatchString(req.Env) {
		return fmt.Errorf("site and environment names: lowercase letters, digits, - and _")
	}
	if !identRe.MatchString(req.Secret) || (req.CookieSecret != "" && !identRe.MatchString(req.CookieSecret)) {
		return fmt.Errorf("secret names: lowercase letters, digits, - and _")
	}
	envs := mapEnsure(mapEnsure(e.sitesNode(), req.Site), "environments")
	if mapGet(envs, req.Env) != nil {
		return fmt.Errorf("%s/%s already exists", req.Site, req.Env)
	}
	if tpl.NeedsUser && strings.TrimSpace(req.User) == "" {
		return fmt.Errorf("this service needs a user")
	}
	env := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if tpl.Postgres {
		if req.Host == "" || req.Database == "" {
			return fmt.Errorf("host and database are required")
		}
		port := req.Port
		if port == 0 {
			port = 5432
		}
		mapSet(env, "host", scalar(req.Host))
		mapSet(env, "port", typedScalar(fmt.Sprint(port), "!!int"))
		mapSet(env, "user", scalar(req.User))
		mapSet(env, "secret_ref", scalar("secret://"+e.key+"/"+req.Secret))
		mapSet(env, "databases", mapping(req.Database, mapping("db", req.Database)))
	} else {
		if !strings.HasPrefix(req.BaseURL, "http://") && !strings.HasPrefix(req.BaseURL, "https://") || strings.Contains(req.BaseURL, "<") {
			return fmt.Errorf("enter the real base URL (http:// or https://)")
		}
		mapSet(env, "base_url", scalar(strings.TrimRight(req.BaseURL, "/")))
		auth := mapping("type", tpl.AuthType)
		if tpl.AuthType == "api-key" {
			header := req.AuthHeader
			if header == "" {
				header = tpl.AuthHeader
			}
			mapSet(auth, "header", scalar(header))
		}
		mapSet(env, "auth", auth)
		if req.User != "" {
			mapSet(env, "user", scalar(req.User))
		}
		mapSet(env, "secret_ref", scalar("secret://"+e.key+"/"+req.Secret))
		if tpl.CookieSecret != "" {
			cookie := req.CookieSecret
			if cookie == "" {
				cookie = tpl.CookieSecret
			}
			mapSet(env, "cookie_secret_ref", scalar("secret://"+e.key+"/"+cookie))
		}
		if req.Insecure {
			mapSet(env, "insecure_skip_verify", typedScalar("true", "!!bool"))
		}
		for _, k := range sortedKeys(req.SiteVars) {
			v := strings.TrimSpace(req.SiteVars[k])
			if !siteVarKeys[k] {
				return fmt.Errorf("unknown site variable %s", k)
			}
			if v != "" {
				mapSet(env, k, scalar(v))
			}
		}
	}
	if req.Tunnel != "" {
		if _, ok := e.parsed.Tunnels[req.Tunnel]; !ok {
			return fmt.Errorf("unknown tunnel %s", req.Tunnel)
		}
		mapSet(env, "requires_tunnel", scalar(req.Tunnel))
	}
	mapSet(envs, req.Env, env)

	ws := e.parsed.Workspaces[e.key]
	if ws.Sites == nil {
		ws.Sites = map[string]config.Site{}
	}
	ws.Sites[req.Site] = config.Site{}
	e.parsed.Workspaces[e.key] = ws

	cat := e.a.catalog()
	for _, id := range req.Ops {
		info, ok := cat[id]
		if !ok {
			return fmt.Errorf("unknown catalog operation %s", id)
		}
		for _, m := range siteVarRe.FindAllStringSubmatch(info.Path, -1) {
			if strings.TrimSpace(req.SiteVars[m[1]]) == "" {
				return fmt.Errorf("%s uses {site.%s} in its path - fill in %s", id, m[1], m[1])
			}
		}
		newID := req.Site + "." + strings.SplitN(id, ".", 2)[1]
		if mapGet(e.opsNode(), newID) == nil {
			if err := e.addOperation(id, info.Source, req.Site, nil); err != nil {
				return err
			}
		}
		if len(req.Roles) > 0 {
			if err := e.addGrants(newID, req.Roles); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *wsEdit) addGrants(id string, roles []string) error {
	rolesNode := e.rolesNode()
	for _, r := range roles {
		role := mapGet(rolesNode, r)
		if role == nil {
			return fmt.Errorf("unknown role %s", r)
		}
		ops := seqEnsure(role, "operations")
		if !seqHas(ops, id) {
			ops.Content = append(ops.Content, scalar(id))
		}
	}
	return nil
}

type agentInfo struct {
	ID         string   `json:"id"`
	Roles      []string `json:"roles"`
	SHAPrefix  string   `json:"sha_prefix"`
	KeyLabel   string   `json:"key_label,omitempty"`
	TokenKnown bool     `json:"token_known"`
}

func (a *app) agents(ws string) ([]agentInfo, []string, error) {
	cfg, err := loadConfig(a.repoConfigPath(ws))
	if err != nil {
		return nil, nil, err
	}
	key := workspaceKey(ws, cfg)
	var out []agentInfo
	for _, ag := range cfg.Agents {
		var roles []string
		for _, g := range ag.Grants {
			if g.Workspace == key {
				roles = append(roles, g.Role)
			}
		}
		if len(roles) == 0 {
			continue
		}
		info := agentInfo{ID: ag.ID, Roles: roles, SHAPrefix: ag.TokenSHA[:min(12, len(ag.TokenSHA))]}
		if k := a.keyBySHA(ag.TokenSHA); k != nil {
			info.KeyLabel, info.TokenKnown = k.Label, k.Token != ""
		}
		out = append(out, info)
	}
	return out, sortedKeys(cfg.Roles), nil
}

func (e *wsEdit) agentNode(id string) *yaml.Node {
	agents := mapGet(e.cfg.root(), "agents")
	if agents == nil {
		return nil
	}
	for _, n := range agents.Content {
		if v := mapGet(n, "id"); v != nil && v.Value == id {
			return n
		}
	}
	return nil
}

// regenerateAgent replaces an agent's token hash and records the new token in agent-keys.txt.
func (a *app) regenerateAgent(ws, id string) (string, error) {
	e, err := a.openEdit(ws)
	if err != nil {
		return "", err
	}
	n := e.agentNode(id)
	if n == nil {
		return "", fmt.Errorf("agent %s not found", id)
	}
	shaNode := mapGet(n, "token_sha256")
	oldSHA := ""
	if shaNode != nil {
		oldSHA = shaNode.Value
	}
	token := randomToken(36)
	sha := sha256Hex(token)
	mapSet(n, "token_sha256", scalar(sha))
	if _, err := e.commit(true); err != nil {
		return "", err
	}
	label := "[" + strings.ToUpper(ws) + " " + id + "]"
	if k := a.keyBySHA(oldSHA); k != nil {
		label = k.Label
	}
	if err := a.upsertKey(label, oldSHA, token, sha, fmt.Sprintf("http://127.0.0.1:%d", a.portFor(ws, e.parsed))); err != nil {
		return token, fmt.Errorf("config updated, but saving agent-keys.txt failed: %w", err)
	}
	return token, nil
}

func (a *app) addAgent(ws, id, role string) (string, error) {
	if !identRe.MatchString(id) {
		return "", fmt.Errorf("agent id: lowercase letters, digits, - and _")
	}
	e, err := a.openEdit(ws)
	if err != nil {
		return "", err
	}
	if e.agentNode(id) != nil {
		return "", fmt.Errorf("agent %s already exists", id)
	}
	if _, ok := e.parsed.Roles[role]; !ok {
		return "", fmt.Errorf("unknown role %s", role)
	}
	token := randomToken(36)
	sha := sha256Hex(token)
	agents := seqEnsure(e.cfg.root(), "agents")
	grants := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{mapping("workspace", e.key, "role", role)}}
	agents.Content = append(agents.Content, mapping("id", id, "token_sha256", typedScalar(sha, "!!str"), "grants", grants))
	if _, err := e.commit(true); err != nil {
		return "", err
	}
	label := "[" + strings.ToUpper(ws) + " " + id + "]"
	if err := a.upsertKey(label, "", token, sha, fmt.Sprintf("http://127.0.0.1:%d", a.portFor(ws, e.parsed))); err != nil {
		return token, fmt.Errorf("config updated, but saving agent-keys.txt failed: %w", err)
	}
	return token, nil
}

// setAdminToken writes a new admin token hash into the source config and records the token.
func (a *app) setAdminToken(ws string) (string, error) {
	e, err := a.openEdit(ws)
	if err != nil {
		return "", err
	}
	token := randomToken(36)
	sha := sha256Hex(token)
	mapSet(e.cfg.root(), "admin_token_sha256", typedScalar(sha, "!!str"))
	if _, err := e.commit(true); err != nil {
		return "", err
	}
	if err := a.upsertKey(adminLabel(ws), "", token, sha, fmt.Sprintf("http://127.0.0.1:%d/admin", a.portFor(ws, e.parsed))); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.adminTokens[ws] = token
	a.mu.Unlock()
	return token, nil
}

// setSiteDisabled soft-deletes (or restores) an integration: its config stays, the gateway refuses its operations.
func (e *wsEdit) setSiteDisabled(site string, disabled bool) error {
	n := mapGet(e.sitesNode(), site)
	if n == nil {
		return fmt.Errorf("workspace %s has no site %q", e.ws, site)
	}
	if disabled {
		mapSet(n, "disabled", typedScalar("true", "!!bool"))
	} else if !mapDelete(n, "disabled") {
		return fmt.Errorf("%s is not disabled", site)
	}
	return nil
}
