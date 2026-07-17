package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Workspaces    map[string]Workspace `yaml:"workspaces"`
	Roles         map[string]Role      `yaml:"roles"`
	Agents        []Agent              `yaml:"agents"`
	Server        ServerConfig         `yaml:"server"`
	Audit         AuditConfig          `yaml:"audit"`
	Tunnels       map[string]Tunnel    `yaml:"tunnels"`
	SecretStore   SecretStoreConfig    `yaml:"secret_store"`
	AdminTokenSHA string               `yaml:"admin_token_sha256"`
	ConfigPath    string               `yaml:"-"`
	OpsPath       string               `yaml:"-"`
}

type SecretStoreConfig struct {
	Backend    string `yaml:"backend"`
	PassDir    string `yaml:"pass_dir"`
	KDBXPath   string `yaml:"kdbx_path"`
	KDBXSecret string `yaml:"kdbx_secret"`
}

type Tunnel struct {
	Type      string `yaml:"type"`  // wireguard, globalprotect, tcp
	Tunnel    string `yaml:"tunnel"`  // e.g. vpn1 for wireguard
	CheckHost string `yaml:"check_host"`  // for tcp checks
	CheckPort int    `yaml:"check_port"`  // for tcp checks
}

type Workspace struct {
	Description string              `yaml:"description"`
	Site    string                 `yaml:"site"`
	Sites   map[string]Site        `yaml:"sites"`
	Policy  WorkspacePolicy        `yaml:"policy"`
	Secrets map[string]string      `yaml:"secrets"`
}

type Site struct {
	Environments map[string]Environment `yaml:"environments"`
}

type Environment struct {
	BaseURL       string                  `yaml:"base_url"`
	Auth          AuthConfig              `yaml:"auth"`
	Host          string                  `yaml:"host"`
	Port          int                     `yaml:"port"`
	ImapHost      string                  `yaml:"imap_host"`
	ImapPort      int                     `yaml:"imap_port"`
	SmtpHost      string                  `yaml:"smtp_host"`
	SmtpPort      int                     `yaml:"smtp_port"`
	ProxyHost     string                  `yaml:"proxy_host"`
	ProxyPort     int                     `yaml:"proxy_port"`
	Workspace     string                  `yaml:"workspace"`
	WorkspaceID   string                  `yaml:"workspace_id"`
	UserID        string                  `yaml:"user_id"`
	Group         string                  `yaml:"group"`
	Org           string                  `yaml:"org"`
	RequiresTunnel string                `yaml:"requires_tunnel"`
	Databases    map[string]Database      `yaml:"databases"`
	Secret       string                  `yaml:"secret"`
	User          string                  `yaml:"user"`
	SecretRef    string                  `yaml:"secret_ref"`
	CookieSecretRef string               `yaml:"cookie_secret_ref"` // second secret (Slack xoxd / Trello token)
	OAuth          *OAuthRefreshConfig   `yaml:"oauth"`
	InsecureSkipVerify bool              `yaml:"insecure_skip_verify"`
}

type Database struct {
	DB         string `yaml:"db"`
	Schema     string `yaml:"schema"`
}

type AuthConfig struct {
	Type   string `yaml:"type"`
	Header string `yaml:"header"`
}

type OAuthRefreshConfig struct {
	TokenURL         string `yaml:"token_url"`
	ClientID         string `yaml:"client_id"`
	ClientSecretRef  string `yaml:"client_secret_ref"`
	RefreshSecretRef string `yaml:"refresh_secret_ref"`
}

type WorkspacePolicy struct {
	JiraProjects      []string `yaml:"jira_projects"`
	BitbucketRepos    []string `yaml:"bitbucket_repos"`
	GitlabProjects    []string `yaml:"gitlab_projects"`
	GithubRepos       []string `yaml:"github_repos"`
	PgAllowedTables   []string `yaml:"pg_allowed_tables"`
	SlackChannels     []string `yaml:"slack_channels"`
	KibanaClusters    []string `yaml:"kibana_clusters"`
	KibanaIndices     []string `yaml:"kibana_indices"`
	ClockifyProjects  []string `yaml:"clockify_projects"`
	EmailAllowedSenders   []string `yaml:"email_allowed_senders"`
	EmailAllowedMailboxes []string `yaml:"email_allowed_mailboxes"`
	EmailDeniedSenders    []string `yaml:"email_denied_senders"`
	EmailDeniedMailboxes  []string `yaml:"email_denied_mailboxes"`
	TrelloAllowedLists    []string `yaml:"trello_allowed_lists"`
}

type Role struct {
	Operations []string `yaml:"operations"`
}

type Agent struct {
	ID       string   `yaml:"id"`
	TokenSHA string   `yaml:"token_sha256"`
	Grants   []Grant  `yaml:"grants"`
}

type Grant struct {
	Workspace string `yaml:"workspace"`
	Role     string `yaml:"role"`
}

type ServerConfig struct {
	ListenAddr string `yaml:"listen_addr"`
	Port       int    `yaml:"port"`
	TLSCert    string `yaml:"tls_cert"`
	TLSKey     string `yaml:"tls_key"`
}

type AuditConfig struct {
	Dir       string `yaml:"dir"`
	MaxSizeMB int    `yaml:"max_size_mb"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	cfg.ConfigPath = path

	return &cfg, nil
}

func (c *Config) Validate() error {
	if len(c.Agents) == 0 {
		return fmt.Errorf("no agents defined")
	}
	for _, agent := range c.Agents {
		if agent.TokenSHA == "" {
			return fmt.Errorf("agent %s has no token_sha256", agent.ID)
		}
		for _, grant := range agent.Grants {
			ws, ok := c.Workspaces[grant.Workspace]
			if !ok {
				return fmt.Errorf("agent %s references unknown workspace %s", agent.ID, grant.Workspace)
			}
			role, ok := c.Roles[grant.Role]
			if !ok {
				return fmt.Errorf("agent %s references unknown role %s", agent.ID, grant.Role)
			}
			for _, op := range role.Operations {
				if !ws.HasOperation(op) {
					return fmt.Errorf("role %s includes operation %s not available in workspace %s", grant.Role, op, grant.Workspace)
				}
			}
		}
	}
	return nil
}

func (w *Workspace) HasOperation(op string) bool {
	if w.Sites == nil {
		return false
	}
	parts := splitOp(op)
	if len(parts) < 2 {
		return false
	}
	site := parts[0]
	if _, ok := w.Sites[site]; !ok {
		return false
	}
	return true
}

func splitOp(op string) []string {
	var parts []string
	current := ""
	for _, c := range op {
		if c == '.' {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func (c *Config) GetAgentByTokenSHA(sha string) *Agent {
	for i := range c.Agents {
		if c.Agents[i].TokenSHA == sha {
			return &c.Agents[i]
		}
	}
	return nil
}

func (c *Config) GetWorkspace(name string) *Workspace {
	ws, ok := c.Workspaces[name]
	if !ok {
		return nil
	}
	return &ws
}

func (c *Config) GetRole(name string) *Role {
	role, ok := c.Roles[name]
	if !ok {
		return nil
	}
	return &role
}

func (c *Config) GetTunnel(name string) *Tunnel {
	if c.Tunnels == nil {
		return nil
	}
	tunnel, ok := c.Tunnels[name]
	if !ok {
		return nil
	}
	return &tunnel
}

func (c *Config) ValidateTunnels() error {
	if c.Tunnels == nil {
		return nil
	}
	for _, ws := range c.Workspaces {
		for _, site := range ws.Sites {
			for _, env := range site.Environments {
				if env.RequiresTunnel != "" {
					if _, ok := c.Tunnels[env.RequiresTunnel]; !ok {
						return fmt.Errorf("workspace %s environment references undefined tunnel: %s", ws.Site, env.RequiresTunnel)
					}
				}
			}
		}
	}
	return nil
}
