//go:build windows

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/tokencheck"
	"gopkg.in/yaml.v3"
)

type workspaceInfo struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Port             int    `json:"port"`
	Service          string `json:"service"`
	ServiceInstalled bool   `json:"service_installed"`
	ServiceState     string `json:"service_state"`
	GatewayUp        bool   `json:"gateway_up"`
	RepoConfig       string `json:"repo_config"`
	RepoOps          string `json:"repo_ops"`
	DeployedConfig   string `json:"deployed_config"`
	Deployed         bool   `json:"deployed"`
	ConfigDrift      bool   `json:"config_drift"`
	AdminConfigured  bool   `json:"admin_configured"`
	AdminTokenSource string `json:"admin_token_source"`
	AdminTokenOK     bool   `json:"admin_token_ok"`
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func loadConfig(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func (a *app) workspaceNames() []string {
	matches, _ := filepath.Glob(filepath.Join(a.buildDir, "config.*.yaml"))
	var names []string
	for _, m := range matches {
		n := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "config."), ".yaml")
		if n != "" && !strings.Contains(n, "example") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func (a *app) repoConfigPath(ws string) string { return filepath.Join(a.buildDir, "config."+ws+".yaml") }
func (a *app) repoOpsPath(ws string) string    { return filepath.Join(a.buildDir, "operations."+ws+".yaml") }
func (a *app) deployedConfigPath(ws string) string {
	return filepath.Join(a.installDir, ws, "config.yaml")
}
func (a *app) deployedOpsPath(ws string) string { return filepath.Join(a.installDir, ws, "operations.yaml") }

// effectiveConfig is what the running gateway uses: the deployed copy when present.
func (a *app) effectiveConfig(ws string) (*config.Config, error) {
	if _, err := os.Stat(a.deployedConfigPath(ws)); err == nil {
		return loadConfig(a.deployedConfigPath(ws))
	}
	return loadConfig(a.repoConfigPath(ws))
}

func (a *app) portFor(ws string, cfg *config.Config) int {
	if cfg.Server.Port != 0 {
		return cfg.Server.Port
	}
	for i, n := range a.workspaceNames() {
		if n == ws {
			return 8443 + i
		}
	}
	return 0
}

func workspaceKey(ws string, cfg *config.Config) string {
	if _, ok := cfg.Workspaces[ws]; ok {
		return ws
	}
	keys := make([]string, 0, len(cfg.Workspaces))
	for k := range cfg.Workspaces {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return keys[0]
	}
	return ws
}

// normalizedEqual compares two config files the way deploy\update.ps1 copies them (trailing whitespace trimmed).
func normalizedEqual(p1, p2 string) bool {
	norm := func(p string) (string, bool) {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", false
		}
		s := strings.TrimPrefix(string(data), string(rune(0xFEFF)))
		s = regexp.MustCompile(`(?m)[\t ]+\r?$`).ReplaceAllString(s, "")
		return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")), true
	}
	a, ok1 := norm(p1)
	b, ok2 := norm(p2)
	return ok1 && ok2 && a == b
}

func (a *app) keysFile() string { return filepath.Join(a.buildDir, "agent-keys.txt") }

func adminLabel(ws string) string { return "[" + strings.ToUpper(ws) + " Admin]" }

type keyEntry struct {
	Label string
	Token string
	SHA   string
}

func (a *app) keyEntries() []keyEntry {
	data, err := os.ReadFile(a.keysFile())
	if err != nil {
		return nil
	}
	var out []keyEntry
	var cur *keyEntry
	labelRe := regexp.MustCompile(`--- Key : (\[.*\]) ---`)
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if m := labelRe.FindStringSubmatch(l); m != nil {
			out = append(out, keyEntry{Label: m[1]})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := regexp.MustCompile(`^\s*Token:\s*(\S+)`).FindStringSubmatch(l); m != nil {
			cur.Token = m[1]
		}
		if m := regexp.MustCompile(`^\s*SHA256:\s*(\S+)`).FindStringSubmatch(l); m != nil {
			cur.SHA = strings.ToLower(m[1])
		}
	}
	return out
}

func (a *app) keyByLabel(label string) string {
	for _, e := range a.keyEntries() {
		if e.Label == label {
			return e.Token
		}
	}
	return ""
}

func (a *app) keyBySHA(sha string) *keyEntry {
	for _, e := range a.keyEntries() {
		if e.SHA == strings.ToLower(sha) || (e.Token != "" && sha256Hex(e.Token) == strings.ToLower(sha)) {
			e := e
			return &e
		}
	}
	return nil
}

// upsertKey writes a key block, replacing an existing block with the same label or the same old SHA.
func (a *app) upsertKey(label, oldSHA, token, sha, endpoint string) error {
	block := fmt.Sprintf("--- Key : %s ---\r\n  Token:    %s\r\n  SHA256:   %s\r\n  Endpoint: %s\r\n  Header:   Authorization: Bearer %s\r\n", label, token, sha, endpoint, token)
	data, _ := os.ReadFile(a.keysFile())
	text := string(data)
	blockRe := regexp.MustCompile(`(?s)--- Key : \[[^\n]*\] ---\r?\n.*?Header:[^\n]*\n`)
	replaced := false
	text = blockRe.ReplaceAllStringFunc(text, func(b string) string {
		if replaced {
			return b
		}
		if strings.Contains(b, "--- Key : "+label+" ---") || (oldSHA != "" && strings.Contains(strings.ToLower(b), strings.ToLower(oldSHA))) {
			replaced = true
			return block
		}
		return b
	})
	if !replaced {
		text = strings.TrimRight(text, "\r\n") + "\r\n\r\n" + block
	}
	return os.WriteFile(a.keysFile(), []byte(text), 0600)
}

func (a *app) adminToken(ws string) (token, source string) {
	a.mu.Lock()
	t := a.adminTokens[ws]
	a.mu.Unlock()
	if t != "" {
		return t, "entered this session"
	}
	if t := a.keyByLabel(adminLabel(ws)); t != "" {
		return t, "agent-keys.txt " + adminLabel(ws)
	}
	return "", ""
}

func (a *app) info(ws string) (workspaceInfo, *config.Config, error) {
	cfg, err := a.effectiveConfig(ws)
	if err != nil {
		return workspaceInfo{Name: ws}, nil, err
	}
	in := workspaceInfo{Name: ws, Port: a.portFor(ws, cfg), Service: "egw-" + ws,
		RepoConfig: a.repoConfigPath(ws), RepoOps: a.repoOpsPath(ws), DeployedConfig: a.deployedConfigPath(ws)}
	if w, ok := cfg.Workspaces[workspaceKey(ws, cfg)]; ok {
		in.Description = w.Description
	}
	_, statErr := os.Stat(in.DeployedConfig)
	in.Deployed = statErr == nil
	if in.Deployed {
		in.ConfigDrift = !normalizedEqual(a.repoConfigPath(ws), a.deployedConfigPath(ws)) || !normalizedEqual(a.repoOpsPath(ws), a.deployedOpsPath(ws))
	}
	in.ServiceInstalled, in.ServiceState = serviceState(in.Service)
	c := http.Client{Timeout: 2 * time.Second}
	if resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", in.Port)); err == nil {
		resp.Body.Close()
		in.GatewayUp = resp.StatusCode == 200
	}
	in.AdminConfigured = cfg.AdminTokenSHA != ""
	token, source := a.adminToken(ws)
	in.AdminTokenSource = source
	in.AdminTokenOK = token != "" && cfg.AdminTokenSHA != "" && sha256Hex(token) == cfg.AdminTokenSHA
	return in, cfg, nil
}

func (a *app) gateway(ws string, method, path string, body interface{}) (int, []byte, error) {
	in, _, err := a.info(ws)
	if err != nil {
		return 0, nil, err
	}
	token, _ := a.adminToken(ws)
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", in.Port, path), rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c := http.Client{Timeout: 120 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("gateway on port %d not reachable: %w", in.Port, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

// reload makes the running gateway re-read its deployed config and operations.
func (a *app) reload(ws string) error {
	status, data, err := a.gateway(ws, "POST", "/admin/reload", nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("reload refused (HTTP %d): %s", status, strings.TrimSpace(string(data)))
	}
	return nil
}

type usage struct {
	Site    string `json:"site"`
	Env     string `json:"env"`
	Role    string `json:"role"`
	User    string `json:"user"`
	BaseURL string `json:"base_url"`
	Tunnel   string `json:"tunnel"`
	Disabled bool   `json:"disabled"`
}

type secretRow struct {
	Name        string             `json:"name"`
	Role        string             `json:"role"`
	Kind        tokencheck.Kind    `json:"kind"`
	Pair        string             `json:"pair"`
	Stored      bool               `json:"stored"`
	LastWritten string             `json:"last_written"`
	UsedBy      []usage            `json:"used_by"`
	Disabled    bool               `json:"disabled"`
	Check       *tokencheck.Result `json:"check"`
	Help        help               `json:"help"`
}

type secretRequest struct {
	WS        string `json:"ws"`
	Name      string `json:"name"`
	Value     string `json:"value"`
	PairValue string `json:"pair_value"`
}

// testSecret checks a candidate value directly against the provider, before it is stored.
func (a *app) testSecret(req secretRequest) (tokencheck.Result, error) {
	cfg, err := a.effectiveConfig(req.WS)
	if err != nil {
		return tokencheck.Result{}, err
	}
	w := cfg.Workspaces[workspaceKey(req.WS, cfg)]
	for _, site := range sortedKeys(w.Sites) {
		if w.Sites[site].Disabled {
			continue
		}
		envs := w.Sites[site].Environments
		for _, envName := range sortedKeys(envs) {
			env := envs[envName]
			kind := tokencheck.DetectKind(site, env)
			if !tokencheck.Checkable(kind) {
				continue
			}
			var primary, secondary string
			switch {
			case strings.HasSuffix(env.SecretRef, "/"+req.Name):
				primary, secondary = req.Value, req.PairValue
			case strings.HasSuffix(env.CookieSecretRef, "/"+req.Name):
				primary, secondary = req.PairValue, req.Value
			default:
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			res := tokencheck.Check(ctx, kind, env, primary, secondary)
			cancel()
			suffix := "tested against " + site + "/" + envName
			if res.Detail == "" {
				res.Detail = suffix
			} else {
				res.Detail += " (" + suffix + ")"
			}
			return res, nil
		}
	}
	return tokencheck.Result{State: tokencheck.StateUnchecked, Detail: "no automatic check for this secret - save it and verify with an operation"}, nil
}

func (a *app) saveSecret(req secretRequest) error {
	value := strings.TrimSpace(req.Value)
	if value == "" || req.Name == "" {
		return fmt.Errorf("name and value required")
	}
	cfg, err := a.effectiveConfig(req.WS)
	if err != nil {
		return err
	}
	status, data, err := a.gateway(req.WS, "POST", "/admin/secrets", map[string]string{"workspace": workspaceKey(req.WS, cfg), "name": req.Name, "value": value})
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("gateway refused (HTTP %d): %s", status, strings.TrimSpace(string(data)))
	}
	return nil
}

func (a *app) useAdminToken(ws, token string, remember bool) error {
	token = strings.TrimSpace(token)
	cfg, err := a.effectiveConfig(ws)
	if err != nil {
		return err
	}
	sha := sha256Hex(token)
	if cfg.AdminTokenSHA == "" || sha != cfg.AdminTokenSHA {
		return fmt.Errorf("this token does not match admin_token_sha256 of the running %s config", ws)
	}
	a.mu.Lock()
	a.adminTokens[ws] = token
	a.mu.Unlock()
	if remember {
		return a.upsertKey(adminLabel(ws), "", token, sha, fmt.Sprintf("http://127.0.0.1:%d/admin", a.portFor(ws, cfg)))
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fileSHA(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
