package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors"
	"github.com/external_gateway/internal/connectors/email"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/connectors/postgres"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/redact"
	"github.com/external_gateway/internal/secrets"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type Server struct {
	cfgPtr        atomic.Pointer[config.Config]
	reg           *policy.OperationRegistry
	logger        zerolog.Logger
	mux           *http.ServeMux
	auditLog      *audit.Logger
	httpClient    *httprest.Connector
	pgConn        *postgres.Connector
	emailConn     *email.Connector
	configChecksum atomic.Value
	opsPath       string
}

func New(cfg *config.Config, reg *policy.OperationRegistry, opsPath string, logger zerolog.Logger) *Server {
	checksum := computeChecksum(cfg.ConfigPath)
	s := &Server{
		reg:      reg,
		logger:   logger,
		mux:      http.NewServeMux(),
		auditLog: audit.GetLogger(),
		httpClient: httprest.NewConnector(),
		pgConn:   postgres.NewConnector(),
		emailConn: email.NewConnector(),
		opsPath:  opsPath,
	}
	s.cfgPtr.Store(cfg)
	s.configChecksum.Store(checksum)
	s.setupRoutes()
	return s
}

func (s *Server) cfg() *config.Config {
	return s.cfgPtr.Load()
}

func computeChecksum(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/whoami", s.handleWhoami)
	s.mux.HandleFunc("/capabilities", s.handleCapabilities)
	s.mux.HandleFunc("/help", s.handleHelp)
	s.mux.HandleFunc("/feedback", s.handleFeedback)
	s.mux.HandleFunc("/v1/workspaces/", s.handleWorkspaceOp)
	s.mux.HandleFunc("/admin/", s.handleAdmin)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agentID := s.authenticate(r)
	if agentID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	agent := s.cfg().GetAgentByTokenSHA(agentID)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	type GrantInfo struct {
		Workspace  string   `json:"workspace"`
		Operations []string `json:"operations"`
	}

	var grants []GrantInfo
	for _, grant := range agent.Grants {
		role := s.cfg().GetRole(grant.Role)
		if role != nil {
			grants = append(grants, GrantInfo{
				Workspace:  grant.Workspace,
				Operations: role.Operations,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"agent_id": agent.ID,
		"grants":   grants,
	})
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agentID := s.authenticate(r)
	if agentID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	agent := s.cfg().GetAgentByTokenSHA(agentID)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	workspace := r.URL.Query().Get("workspace")
	if workspace == "" {
		http.Error(w, "workspace query param required", http.StatusBadRequest)
		return
	}

	hasAccess := false
	for _, grant := range agent.Grants {
		if grant.Workspace == workspace {
			hasAccess = true
			break
		}
	}
	if !hasAccess {
		http.Error(w, "workspace not accessible", http.StatusForbidden)
		return
	}

	ws := s.cfg().GetWorkspace(workspace)
	if ws == nil {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}

	type OpCapability struct {
		ID       string            `json:"id"`
		Params   map[string]any    `json:"params"`
		Site     string            `json:"site"`
		Method   string            `json:"method,omitempty"`
		Path     string            `json:"path,omitempty"`
		Example  string            `json:"example"`
	}

	var ops []OpCapability
	wsOps := s.getWorkspaceOperations(ws)

	for _, grant := range agent.Grants {
		if grant.Workspace != workspace {
			continue
		}
		role := s.cfg().GetRole(grant.Role)
		if role == nil {
			continue
		}
		for _, opID := range role.Operations {
			if op, ok := s.reg.Get(opID); ok {
				wsOps[opID] = op
			}
		}
	}

	for id, op := range wsOps {
		params := make(map[string]any)
		for name, def := range op.Params {
			params[name] = map[string]any{
				"type":     def.Type,
				"required": def.Required,
			}
			if def.Values != nil {
				params[name].(map[string]any)["values"] = def.Values
			}
			if def.MaxLen > 0 {
				params[name].(map[string]any)["maxlen"] = def.MaxLen
			}
		}
		ops = append(ops, OpCapability{
			ID:     id,
			Params: params,
			Site:   op.Site,
			Method: op.Method,
			Path:   op.Path,
			Example: s.buildExample(workspace, id, op.Params),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"workspace":      workspace,
		"operations":     ops,
		"policy":         ws.Policy,
		"config_checksum": s.configChecksum.Load().(string),
		"features": map[string]string{
			"postgres_ast_parsing":    "semicolon-multi-statement-detect",
			"response_field_filter":   "enabled",
			"admin_auth":              map[bool]string{true: "enabled", false: "unconfigured"}[s.cfg().AdminTokenSHA != ""],
			"cross_tenant_isolation":  "enabled",
			"config_signing":          "sha256-checksum",
			"config_hot_reload":       "enabled",
			"clockify_scheduler":      "stub-not-wired",
			"secret_empty_detect":     "enabled",
		},
	})
}

func (s *Server) getWorkspaceOperations(ws *config.Workspace) map[string]*policy.Operation {
	ops := make(map[string]*policy.Operation)
	for id, op := range s.reg.Operations {
		if op.Site == "" {
			continue
		}
		if _, ok := ws.Sites[op.Site]; ok {
			ops[id] = op
		}
	}
	return ops
}

func (s *Server) buildExample(workspace, opID string, params map[string]policy.ParamDef) string {
	port := s.cfg().Server.Port
	if port == 0 {
		port = 8443
	}
	var bodyParts []string
	for name, def := range params {
		if name == "env" {
			continue
		}
		val := `"` + name + `_value"`
		switch def.Type {
		case "bool":
			val = "$true"
		case "int":
			val = "42"
		}
		bodyParts = append(bodyParts, fmt.Sprintf(`%s=%s`, name, val))
	}
	body := "@{env=`\"prod`\";" + strings.Join(bodyParts, ";") + "} | ConvertTo-Json"
	return fmt.Sprintf(
		`$h=@{Authorization="Bearer <token>"}; Invoke-RestMethod -Uri "http://127.0.0.1:%d/v1/workspaces/%s/op/%s" -Method Post -Body (%s) -ContentType "application/json" -Headers $h`,
		port, workspace, opID, body,
	)
}

func (s *Server) handleWorkspaceOp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agentID := s.authenticate(r)
	if agentID == "" {
		s.writeDeny(w, r, "", "", "", "authentication", "missing or invalid token")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/workspaces/")
	parts := strings.SplitN(path, "/", 4)
	if len(parts) < 3 || parts[0] == "" || parts[1] != "op" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	workspace := parts[0]
	opID := parts[2]

	if r.URL.Query().Get("dry_run") == "true" {
		s.handleDryRun(w, r, agentID, workspace, opID)
		return
	}

	s.executeOp(w, r, agentID, workspace, opID)
}

func (s *Server) handleDryRun(w http.ResponseWriter, r *http.Request, agentID, workspace, opID string) {
	agent := s.cfg().GetAgentByTokenSHA(agentID)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	ws := s.cfg().GetWorkspace(workspace)
	if ws == nil {
		s.writeDeny(w, r, agentID, workspace, opID, "workspace", "workspace not found")
		return
	}

	op, ok := s.reg.Get(opID)
	if !ok {
		s.writeDeny(w, r, agentID, workspace, opID, "operation", "operation not found")
		return
	}

	if !s.agentHasOp(agent, workspace, opID) {
		s.writeDeny(w, r, agentID, workspace, opID, "grant", "operation not allowed for this agent")
		return
	}

	var params map[string]interface{}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}

	if err := s.reg.ValidateParams(op, params, ws); err != nil {
		s.writeDeny(w, r, agentID, workspace, opID, "validation", err.Error())
		return
	}

	if err := s.reg.CheckConstraints(op, params, ws); err != nil {
		s.writeDeny(w, r, agentID, workspace, opID, "constraint", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"decision": "allow",
		"message":  "dry run - no secret resolved, no upstream called",
	})
}

func (s *Server) executeOp(w http.ResponseWriter, r *http.Request, agentID, workspace, opID string) {
	requestID := uuid.New().String()
	start := time.Now()

	agent := s.cfg().GetAgentByTokenSHA(agentID)
	if agent == nil {
		s.writeDeny(w, r, agentID, workspace, opID, "agent", "agent not found")
		return
	}

	ws := s.cfg().GetWorkspace(workspace)
	if ws == nil {
		s.writeDeny(w, r, agentID, workspace, opID, "workspace", "workspace not found")
		return
	}

	op, ok := s.reg.Get(opID)
	if !ok {
		s.writeDeny(w, r, agentID, workspace, opID, "operation", "operation not found")
		return
	}

	if !s.agentHasOp(agent, workspace, opID) {
		s.writeDeny(w, r, agentID, workspace, opID, "grant", "operation not allowed for this agent")
		return
	}

	var params map[string]interface{}
	if r.Body != nil {
		body, readErr := io.ReadAll(r.Body)
		if len(body) > 0 {
			if err := json.Unmarshal(body, &params); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
		}
		_ = readErr
	}

	if err := s.reg.ValidateParams(op, params, ws); err != nil {
		s.writeDeny(w, r, agentID, workspace, opID, "validation", err.Error())
		return
	}

	if err := s.reg.CheckConstraints(op, params, ws); err != nil {
		s.writeDeny(w, r, agentID, workspace, opID, "constraint", err.Error())
		return
	}

	if err := s.checkTunnelAccess(ws, op, params); err != nil {
		s.writeDeny(w, r, agentID, workspace, opID, "tunnel", err.Error())
		return
	}

	s.reg.ApplyDefaults(op, params)

	var upstreamStatus int
	var responseBody []byte
	var err error

	if op.Connector == "http-rest" {
		upstreamStatus, responseBody, err = s.executeHttpConnector(ws, op, params, workspace)
	} else if op.Connector == "postgres" {
		upstreamStatus, responseBody, err = s.executePostgresConnector(ws, op, params, workspace)
	} else if op.Connector == "local-service" {
		responseBody, err = s.executeLocalServiceConnector(ws, op, params)
		upstreamStatus = 200
	} else if op.Connector == "mcp" {
		upstreamStatus, responseBody, err = s.executeMcpConnector(ws, op, params, workspace)
	} else if op.Connector == "email" {
		upstreamStatus, responseBody, err = s.executeEmailConnector(ws, op, params, workspace)
	}

	latency := time.Since(start).Milliseconds()
	s.auditLog.LogAllow(requestID, agentID, workspace, opID, params, latency, upstreamStatus)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestID)

	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"request_id":  requestID,
			"status":      "error",
			"operation":   opID,
			"workspace":   workspace,
			"error":       err.Error(),
		})
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		result = map[string]interface{}{
			"request_id": requestID,
			"operation":  opID,
			"workspace":  workspace,
			"upstream_status": upstreamStatus,
			"upstream_body":    string(responseBody),
		}
		json.NewEncoder(w).Encode(result)
		return
	}

	result["request_id"] = requestID
	result["operation"] = opID
	result["workspace"] = workspace
	json.NewEncoder(w).Encode(result)
}

func (s *Server) agentHasOp(agent *config.Agent, workspace, opID string) bool {
	for _, grant := range agent.Grants {
		if grant.Workspace != workspace {
			continue
		}
		role := s.cfg().GetRole(grant.Role)
		if role == nil {
			continue
		}
		for _, allowed := range role.Operations {
			if allowed == opID {
				return true
			}
		}
	}
	return false
}

func (s *Server) checkTunnelAccess(ws *config.Workspace, op *policy.Operation, params map[string]interface{}) error {
	envName := ""
	if val, ok := params["env"].(string); ok {
		envName = val
	} else if val, ok := params["cluster"].(string); ok {
		envName = val
	}

	if envName == "" {
		return nil
	}

	site, ok := ws.Sites[op.Site]
	if !ok {
		return nil
	}

	env, ok := site.Environments[envName]
	if !ok {
		return nil
	}

	if env.RequiresTunnel == "" {
		return nil
	}

	tunnel := s.cfg().GetTunnel(env.RequiresTunnel)
	if tunnel == nil {
		return fmt.Errorf("required tunnel %s not configured", env.RequiresTunnel)
	}

	if err := s.reg.CheckTunnel(env.RequiresTunnel); err != nil {
		return fmt.Errorf("required tunnel %s is down: bring it up manually", env.RequiresTunnel)
	}

	return nil
}

func (s *Server) executeHttpConnector(ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error) {
	site, ok := ws.Sites[op.Site]
	if !ok {
		return 0, nil, fmt.Errorf("site %s not found in workspace", op.Site)
	}

	envName := ""
	if val, ok := params["env"].(string); ok {
		envName = val
	} else if val, ok := params["cluster"].(string); ok {
		envName = val
	}

	var env *config.Environment
	if envName != "" {
		if e, ok := site.Environments[envName]; ok {
			env = &e
		}
	}

	if env == nil {
		for name := range site.Environments {
			if e, ok := site.Environments[name]; ok {
				env = &e
				break
			}
		}
	}

	if env == nil {
		return 0, nil, fmt.Errorf("no environment found for site %s", op.Site)
	}

	baseURL := env.BaseURL
	if env.ProxyHost != "" {
		scheme := "http"
		if strings.HasPrefix(baseURL, "https://") {
			scheme = "https"
		}
		proxyPort := env.ProxyPort
		if proxyPort == 0 {
			if scheme == "https" { proxyPort = 443 } else { proxyPort = 80 }
		}
		baseURL = fmt.Sprintf("%s://%s:%d", scheme, env.ProxyHost, proxyPort)
	}
	if baseURL == "" {
		return 0, nil, fmt.Errorf("no base_url configured for environment %q — set it in config to enable this operation", envName)
	}
	if baseURL == "" || baseURL == "-" {
		return 0, nil, fmt.Errorf("base_url for environment %q is not configured (empty or placeholder); update config.<ws>.yaml and deploy", envName)
	}

	path := op.Path
	path = strings.Replace(path, "{site.workspace_id}", env.WorkspaceID, -1)
	path = strings.Replace(path, "{site.user_id}", env.UserID, -1)
	path = strings.Replace(path, "{site.workspace}", env.Workspace, -1)
	path = strings.Replace(path, "{site.org}", env.Org, -1)
	path = strings.Replace(path, "{site.group}", env.Group, -1)
	for key, val := range params {
		strVal := fmt.Sprintf("%v", val)
		if strings.HasSuffix(key, "|urlencode") {
			strVal = url.PathEscape(strVal)
			key = strings.TrimSuffix(key, "|urlencode")
		}

		encKey := key + "|urlencode"
		if strings.Contains(op.Path, "{"+encKey+"}") {
			encodedVal := url.PathEscape(fmt.Sprintf("%v", val))
			if strings.Contains(op.Path, "?path={"+encKey+"}") || strings.Contains(op.Path, "&path={"+encKey+"}") {
				encodedVal = url.QueryEscape(fmt.Sprintf("%v", val))
			}
			path = strings.Replace(path, "{"+encKey+"}", encodedVal, -1)
		}

		if strings.Contains(op.Path, "?path={"+key+"}") || strings.Contains(op.Path, "&path={"+key+"}") {
			strVal = url.QueryEscape(strVal)
		}
		path = strings.Replace(path, "{"+key+"}", strVal, -1)
	}

	queryParams := url.Values{}
	for key, val := range params {
		if key == "env" || key == "cluster" {
			continue
		}
		if strings.Contains(op.Path, "{"+key+"}") || strings.Contains(op.Path, "{"+key+"|urlencode}") {
			continue
		}
		queryParams.Add(key, formatParamValue(val))
	}

	urlStr := baseURL + path
	if encoded := queryParams.Encode(); encoded != "" {
		if strings.Contains(path, "?") {
			urlStr += "&" + encoded
		} else {
			urlStr += "?" + encoded
		}
	}

	var body []byte
	var err error
	if op.BodyTemplate != "" {
		body, err = s.buildBody(op.BodyTemplate, params)
		if err != nil {
			return 0, nil, fmt.Errorf("building body: %w", err)
		}
	}

	s.logger.Info().
		Str("url", urlStr).
		Str("method", op.Method).
		Str("body", string(body)).
		Msg("upstream request")

	headers := map[string]string{
		"Accept": "application/json",
		"Content-Type": "application/json",
	}

	if op.Site == "kibana" {
		headers["kbn-xsrf"] = "true"
	}

	// Jenkins CSRF crumb: POST requests need a valid crumb. Fetch it before the main
	// request by calling the crumbIssuer endpoint with the same auth headers.
	if op.Site == "jenkins" && (op.Method == "POST" || op.Method == "PUT") && env.SecretRef != "" {
		crumbURL := strings.TrimSuffix(baseURL, "/") + "/crumbIssuer/api/json"
		crumbHeaders := map[string]string{"Accept": "application/json"}
		wsName, secretName := parseSecretRef(env.SecretRef)
		if secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace); err == nil && string(secret) != "" {
			auth := base64Auth(env.User, strings.TrimSpace(string(secret)))
			crumbHeaders["Authorization"] = "Basic " + auth
		}
		if crumbResp, crumbErr := s.httpClient.Execute(context.Background(), &connectors.Request{
			Method:   "GET",
			Path:     crumbURL,
			Headers:  crumbHeaders,
			Insecure: env.InsecureSkipVerify,
		}); crumbErr == nil && crumbResp.StatusCode == 200 {
			var crumbData struct {
				Crumb            string `json:"crumb"`
				CrumbRequestField string `json:"crumbRequestField"`
			}
			if json.Unmarshal(crumbResp.Body, &crumbData) == nil && crumbData.Crumb != "" {
				headers[crumbData.CrumbRequestField] = crumbData.Crumb
				s.logger.Info().Str("crumb_field", crumbData.CrumbRequestField).Msg("Jenkins CSRF crumb fetched")
			}
		} else {
			if crumbErr != nil {
				s.logger.Warn().Err(crumbErr).Msg("Jenkins crumb fetch failed")
			} else {
				s.logger.Warn().Int("status", crumbResp.StatusCode).Msg("Jenkins crumb fetch returned non-200")
			}
		}
	}

	s.logger.Info().
		Str("url", urlStr).
		Str("method", op.Method).
		Msg("executing upstream request")

	if env.Auth.Type == "basic" && env.User != "" {
		passwd, _ := params["password"].(string)
		secretName := ""
		if passwd == "" {
			wsName, secretName := parseSecretRef(env.SecretRef)
			if secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace); err == nil {
				passwd = string(secret)
			} else {
				s.logger.Error().Err(err).Str("secret_ref", env.SecretRef).Msg("failed to resolve secret")
			}
		}
		if passwd != "" {
			auth := base64Auth(env.User, passwd)
			s.logger.Info().Str("user", env.User).Str("secret_name", secretName).Msg("basic auth resolved")
			headers["Authorization"] = "Basic " + auth
		}
	} else if env.Auth.Type == "bearer" && env.SecretRef != "" {
		wsName, secretName := parseSecretRef(env.SecretRef)
		if secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace); err == nil {
			headers["Authorization"] = "Bearer " + string(secret)
		} else {
			s.logger.Error().Err(err).Str("secret_ref", env.SecretRef).Msg("failed to resolve secret")
		}
	} else if env.Auth.Type == "api-key" && env.SecretRef != "" {
		wsName, secretName := parseSecretRef(env.SecretRef)
		if secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace); err == nil {
			headers[env.Auth.Header] = string(secret)
		} else {
			s.logger.Error().Err(err).Str("secret_ref", env.SecretRef).Msg("failed to resolve secret")
		}
	} else if env.Auth.Type == "slack-session" && env.SecretRef != "" {
		// Slack browser/desktop session auth: xoxc token as Bearer + xoxd cookie as `d`.
		// Both resolved server-side from WinCred; the agent never sees either value.
		// NOTE: pass `workspace` (the request workspace) — not ws.Site, which is empty
		// for map-based workspaces and would trip the cross-tenant check.
		tWs, tName := parseSecretRef(env.SecretRef)
		if tok, err := secrets.ResolveWithWorkspaceCheck(tWs, tName, workspace); err == nil {
			headers["Authorization"] = "Bearer " + strings.TrimSpace(string(tok))
		} else {
			s.logger.Error().Err(err).Str("secret_ref", env.SecretRef).Msg("failed to resolve slack token secret")
		}
		if env.CookieSecretRef != "" {
			cWs, cName := parseSecretRef(env.CookieSecretRef)
			if cook, err := secrets.ResolveWithWorkspaceCheck(cWs, cName, workspace); err == nil {
				headers["Cookie"] = "d=" + strings.TrimSpace(string(cook))
			} else {
				s.logger.Error().Err(err).Str("cookie_secret_ref", env.CookieSecretRef).Msg("failed to resolve slack cookie secret")
			}
		}
	} else if env.Auth.Type == "trello" && env.SecretRef != "" {
		wsName, secretName := parseSecretRef(env.SecretRef)
		secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace)
		if err != nil {
			s.logger.Error().Err(err).Str("secret_ref", env.SecretRef).Msg("failed to resolve trello key")
		} else {
			sep := "?"
			if strings.Contains(urlStr, "?") {
				sep = "&"
			}
			urlStr += fmt.Sprintf("%skey=%s", sep, url.QueryEscape(strings.TrimSpace(string(secret))))
			if env.CookieSecretRef != "" {
				tWs, tName := parseSecretRef(env.CookieSecretRef)
				if token, err := secrets.ResolveWithWorkspaceCheck(tWs, tName, workspace); err == nil {
					urlStr += "&token=" + url.QueryEscape(strings.TrimSpace(string(token)))
				} else {
					s.logger.Error().Err(err).Str("cookie_secret_ref", env.CookieSecretRef).Msg("failed to resolve trello token")
				}
			}
		}
	}

	// Trello list-scoped write protection: when trello_allowed_lists is configured,
	// operations that target a list must name an allowed list. Ops without a listId
	// param are denied (e.g. add_comment by card ID — fetch the card first to scope it).
	if op.Site == "trello" && len(ws.Policy.TrelloAllowedLists) > 0 {
		listID, _ := params["listId"].(string)
		if listID == "" {
			listID, _ = params["idList"].(string)
		}
		if listID == "" {
			return 0, nil, fmt.Errorf("list-scoped trello operation %s requires a listId or idList parameter; trello_allowed_lists is configured", op.ID)
		}
		allowed := false
		for _, lid := range ws.Policy.TrelloAllowedLists {
			if lid == listID {
				allowed = true
				break
			}
		}
		if !allowed {
			return 0, nil, fmt.Errorf("list %s is not in trello_allowed_lists; operation %s denied", listID, op.ID)
		}
	}

	resp, err := s.httpClient.Execute(context.Background(), &connectors.Request{
		Method:   op.Method,
		Path:     urlStr,
		Headers:  headers,
		Body:     body,
		Insecure: env.InsecureSkipVerify,
	})

	if err != nil {
		s.logger.Error().Err(err).Str("url", urlStr).Msg("upstream request failed")
		return 0, nil, fmt.Errorf("upstream request failed: %w", err)
	}

	// OAuth auto-refresh: if bearer auth returns 401 and OAuth config is present,
	// use the refresh token to get a new access token, store it, and retry once.
	if resp.StatusCode == http.StatusUnauthorized && env.Auth.Type == "bearer" && env.OAuth != nil && env.SecretRef != "" {
		s.logger.Info().Str("site", op.Site).Str("url", urlStr).Msg("received 401, attempting OAuth token refresh")
		if s.refreshOAuthToken(env, workspace) {
			wsName, secretName := parseSecretRef(env.SecretRef)
			if secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace); err == nil {
				headers["Authorization"] = "Bearer " + strings.TrimSpace(string(secret))
				resp, err = s.httpClient.Execute(context.Background(), &connectors.Request{
					Method:   op.Method,
					Path:     urlStr,
					Headers:  headers,
					Body:     body,
					Insecure: env.InsecureSkipVerify,
				})
				if err != nil {
					s.logger.Error().Err(err).Str("url", urlStr).Msg("upstream request failed after token refresh")
					return 0, nil, fmt.Errorf("upstream request failed after token refresh: %w", err)
				}
				s.logger.Info().Str("site", op.Site).Int("status", resp.StatusCode).Msg("retry after OAuth token refresh")
			}
		}
	}

	s.logger.Info().
		Str("url", urlStr).
		Int("status", resp.StatusCode).
		Int("body_len", len(resp.Body)).
		Str("body_preview", string(resp.Body[:min(200, len(resp.Body))])).
		Msg("upstream response")

	if len(op.Response.AllowFields) > 0 {
		var jsonData map[string]interface{}
		if err := json.Unmarshal(resp.Body, &jsonData); err == nil {
			shaper := redact.NewResponseShaper(op.Response.AllowFields)
			filtered := shaper.Shape(jsonData)
			if filteredBody, err := json.Marshal(filtered); err == nil {
				resp.Body = filteredBody
				s.logger.Info().Int("original", len(resp.Body)).Int("filtered", len(filteredBody)).Msg("response shaping applied")
			}
		}
	}

	return resp.StatusCode, resp.Body, nil
}

// executeMcpConnector fronts a remote MCP server (JSON-RPC 2.0 over Streamable HTTP,
// e.g. the Atlassian Rovo MCP server). The credential stays isolated in egw: it is
// resolved from Credential Manager and injected into the Authorization header here,
// never handed to the agent. Because MCP is stateful, each op call runs the full
// handshake: initialize -> capture Mcp-Session-Id -> notifications/initialized ->
// tools/list or tools/call. Responses may be JSON or SSE (text/event-stream).
//
// The op's `path` selects the action: "tools/list" lists tools; anything else is the
// tool name for tools/call (or the caller supplies a `tool` param). Arguments come
// from `body_template` (rendered) or an `arguments` param (raw JSON).
func (s *Server) executeMcpConnector(ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error) {
	site, ok := ws.Sites[op.Site]
	if !ok {
		return 0, nil, fmt.Errorf("site %s not found in workspace", op.Site)
	}

	var env *config.Environment
	if envName, ok := params["env"].(string); ok && envName != "" {
		if e, ok := site.Environments[envName]; ok {
			env = &e
		}
	}
	if env == nil {
		for name := range site.Environments {
			e := site.Environments[name]
			env = &e
			break
		}
	}
	if env == nil {
		return 0, nil, fmt.Errorf("no environment found for site %s", op.Site)
	}
	endpoint := env.BaseURL
	if endpoint == "" || endpoint == "-" {
		return 0, nil, fmt.Errorf("no base_url configured for MCP site %s", op.Site)
	}

	authHeader, err := resolveMcpAuthHeader(env, workspace)
	if err != nil {
		return 0, nil, err
	}

	headers := map[string]string{
		"Accept":       "application/json, text/event-stream",
		"Content-Type": "application/json",
	}
	if authHeader != "" {
		headers["Authorization"] = authHeader
	}

	// 1. initialize — capture the session id.
	initBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"egw","version":"1.0"}}}`)
	initResp, err := s.httpClient.Execute(context.Background(), &connectors.Request{Method: "POST", Path: endpoint, Headers: headers, Body: initBody})
	if err != nil {
		return 0, nil, fmt.Errorf("mcp initialize failed: %w", err)
	}
	if initResp.StatusCode >= 400 {
		return initResp.StatusCode, nil, fmt.Errorf("mcp initialize HTTP %d: %s", initResp.StatusCode, string(initResp.Body[:min(300, len(initResp.Body))]))
	}
	sessionID := headerLookup(initResp.Headers, "Mcp-Session-Id")

	sessHeaders := map[string]string{}
	for k, v := range headers {
		sessHeaders[k] = v
	}
	if sessionID != "" {
		sessHeaders["Mcp-Session-Id"] = sessionID
	}

	// 2. notifications/initialized (best-effort; server returns 202/no content).
	_, _ = s.httpClient.Execute(context.Background(), &connectors.Request{Method: "POST", Path: endpoint, Headers: sessHeaders, Body: []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)})

	// 3. tools/list or tools/call.
	var rpcBody []byte
	if op.Path == "tools/list" {
		rpcBody = []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	} else {
		toolName := op.Path
		if toolName == "" {
			toolName, _ = params["tool"].(string)
		}
		if toolName == "" {
			return 0, nil, fmt.Errorf("mcp op %s: no tool name (set op path or a 'tool' param)", op.ID)
		}
		var argsRaw json.RawMessage
		if op.BodyTemplate != "" {
			b, berr := s.buildBody(op.BodyTemplate, params)
			if berr != nil {
				return 0, nil, fmt.Errorf("building mcp arguments: %w", berr)
			}
			argsRaw = json.RawMessage(b)
		} else if a, ok := params["arguments"].(string); ok && a != "" {
			argsRaw = json.RawMessage(a)
		} else {
			argsRaw = json.RawMessage("{}")
		}
		callParams, _ := json.Marshal(map[string]interface{}{"name": toolName, "arguments": argsRaw})
		rpcBody, _ = json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": json.RawMessage(callParams)})
	}

	callResp, err := s.httpClient.Execute(context.Background(), &connectors.Request{Method: "POST", Path: endpoint, Headers: sessHeaders, Body: rpcBody})
	if err != nil {
		return 0, nil, fmt.Errorf("mcp call failed: %w", err)
	}

	result, rpcErrMsg, perr := parseJSONRPCResult(headerLookup(callResp.Headers, "Content-Type"), callResp.Body)
	if perr != nil {
		return callResp.StatusCode, nil, fmt.Errorf("parsing mcp response (HTTP %d): %w; body: %s", callResp.StatusCode, perr, string(callResp.Body[:min(300, len(callResp.Body))]))
	}
	if rpcErrMsg != "" {
		return callResp.StatusCode, nil, fmt.Errorf("mcp error: %s", rpcErrMsg)
	}

	body := []byte(result)
	if len(op.Response.AllowFields) > 0 {
		var jsonData map[string]interface{}
		if err := json.Unmarshal(body, &jsonData); err == nil {
			shaper := redact.NewResponseShaper(op.Response.AllowFields)
			if filtered, err := json.Marshal(shaper.Shape(jsonData)); err == nil {
				body = filtered
			}
		}
	}
	return callResp.StatusCode, body, nil
}

// resolveMcpAuthHeader builds the Authorization header for an MCP site from the
// site's configured credential (never exposed to the agent). Supports Atlassian's
// API-token auth: personal token via Basic base64(email:token), or service-account
// key via Bearer.
func resolveMcpAuthHeader(env *config.Environment, workspace string) (string, error) {
	switch env.Auth.Type {
	case "basic":
		wsName, secretName := parseSecretRef(env.SecretRef)
		secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace)
		if err != nil {
			return "", fmt.Errorf("resolving mcp secret: %w", err)
		}
		return "Basic " + base64Auth(env.User, string(secret)), nil
	case "bearer":
		wsName, secretName := parseSecretRef(env.SecretRef)
		secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace)
		if err != nil {
			return "", fmt.Errorf("resolving mcp secret: %w", err)
		}
		return "Bearer " + strings.TrimSpace(string(secret)), nil
	}
	return "", nil
}

func headerLookup(h map[string]string, key string) string {
	if v, ok := h[key]; ok {
		return v
	}
	for k, v := range h {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// parseJSONRPCResult extracts the JSON-RPC `result` (and any error message) from an
// MCP response body, handling both application/json and text/event-stream (SSE).
func parseJSONRPCResult(contentType string, body []byte) (json.RawMessage, string, error) {
	payload := body
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		var parts []string
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.HasPrefix(line, "data:") {
				parts = append(parts, strings.TrimSpace(line[len("data:"):]))
			}
		}
		payload = []byte(strings.Join(parts, ""))
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return nil, "", err
	}
	if rpc.Error != nil {
		return nil, rpc.Error.Message, nil
	}
	return rpc.Result, "", nil
}

func (s *Server) executePostgresConnector(ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error) {
	site, ok := ws.Sites[op.Site]
	if !ok {
		return 0, nil, fmt.Errorf("site %s not found", op.Site)
	}

	envName, _ := params["env"].(string)
	env, ok := site.Environments[envName]
	if !ok {
		return 0, nil, fmt.Errorf("environment %s not found", envName)
	}

	dbName, _ := params["db"].(string)

	db, ok := env.Databases[dbName]
	if !ok {
		return 0, nil, fmt.Errorf("database %s not found in env %s", dbName, envName)
	}

	host := env.Host
	port := env.Port
	if env.ProxyHost != "" {
		host = env.ProxyHost
	}
	if env.ProxyPort != 0 {
		port = env.ProxyPort
	}

	wsName, secretName := parseSecretRef(env.SecretRef)
	secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace)
	if err != nil {
		return 0, nil, fmt.Errorf("resolving secret: %w", err)
	}

	if err := s.pgConn.Connect(host, port, env.User, string(secret), db.DB, db.Schema); err != nil {
		return 0, nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	defer s.pgConn.Close()

	var result *postgres.QueryResult
	switch op.ID {
	case "postgres.query":
		sqlQuery, _ := params["sql"].(string)
		if sqlQuery == "" {
			return 0, nil, fmt.Errorf("sql parameter required")
		}
		result, err = s.pgConn.ExecuteQuery(context.Background(), sqlQuery)
	case "postgres.tables":
		result, err = s.pgConn.ExecuteQuery(context.Background(),
			"SELECT table_name, table_type FROM information_schema.tables WHERE table_schema = "+postgres.QuoteLiteral(db.Schema)+" ORDER BY table_name")
	case "postgres.schema":
		tableName, _ := params["table"].(string)
		if tableName == "" {
			return 0, nil, fmt.Errorf("table parameter required")
		}
		result, err = s.pgConn.ExecuteQuery(context.Background(),
			"SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = "+postgres.QuoteLiteral(db.Schema)+" AND table_name = "+postgres.QuoteLiteral(tableName)+" ORDER BY ordinal_position")
	default:
		return 0, nil, fmt.Errorf("unknown postgres operation: %s", op.ID)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("query failed: %w", err)
	}

	output := map[string]interface{}{
		"columns": result.Columns,
		"rows":    result.Rows,
		"count":   result.Count,
	}
	body, _ := json.Marshal(output)
	return 200, body, nil
}

func (s *Server) executeLocalServiceConnector(ws *config.Workspace, op *policy.Operation, params map[string]interface{}) ([]byte, error) {
	tunnelName, _ := params["tunnel"].(string)
	if tunnelName == "" {
		tunnelName = "vpn1"
	}

	tunCfg := s.cfg().GetTunnel(tunnelName)
	if tunCfg == nil {
		return nil, fmt.Errorf("tunnel %s not configured", tunnelName)
	}

	switch op.Method {
	case "STATUS":
		if _, ok := ws.Sites[op.Site]; !ok {
			return nil, fmt.Errorf("site %s not found", op.Site)
		}
		svcName := fmt.Sprintf("WireGuardTunnel$%s", tunCfg.Tunnel)
		cmd := exec.CommandContext(context.Background(), "sc", "query", svcName)
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("service query failed: %v", err)
		}
		running := strings.Contains(string(output), "RUNNING")
		return json.Marshal(map[string]interface{}{
			"tunnel":    tunnelName,
			"type":      tunCfg.Type,
			"connected": running,
			"details":   fmt.Sprintf("WireGuardTunnel$%s status: %s", tunCfg.Tunnel, string(output)),
		})
	case "TEST":
		host, _ := params["host"].(string)
		portStr, _ := params["port"].(string)
		port, _ := strconv.Atoi(portStr)
		if host == "" || port == 0 {
			return nil, fmt.Errorf("host and port required")
		}
		target := net.JoinHostPort(host, strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", target, 5*time.Second)
		if err != nil {
			return json.Marshal(map[string]interface{}{
				"host":     host,
				"port":     port,
				"reachable": false,
				"error":    err.Error(),
			})
		}
		conn.Close()
		return json.Marshal(map[string]interface{}{
			"host":      host,
			"port":      port,
			"reachable": true,
		})
	default:
		return nil, fmt.Errorf("unknown local-service method: %s", op.Method)
	}
}

func base64Auth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}

func parseSecretRef(ref string) (workspace, name string) {
	if len(ref) < 9 || ref[:9] != "secret://" {
		return "", ref
	}
	rest := ref[9:]
	slashIdx := strings.Index(rest, "/")
	if slashIdx == -1 {
		return "", rest
	}
	return rest[:slashIdx], rest[slashIdx+1:]
}

func (s *Server) refreshOAuthToken(env *config.Environment, workspace string) bool {
	refWs, refName := parseSecretRef(env.OAuth.RefreshSecretRef)
	refreshToken, err := secrets.ResolveWithWorkspaceCheck(refWs, refName, workspace)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to resolve OAuth refresh token")
		return false
	}
	var clientSecret string
	if env.OAuth.ClientSecretRef != "" {
		csWs, csName := parseSecretRef(env.OAuth.ClientSecretRef)
		if cs, err := secrets.ResolveWithWorkspaceCheck(csWs, csName, workspace); err == nil {
			clientSecret = strings.TrimSpace(string(cs))
		}
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", strings.TrimSpace(string(refreshToken)))
	form.Set("client_id", env.OAuth.ClientID)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	tokenResp, err := http.PostForm(env.OAuth.TokenURL, form)
	if err != nil {
		s.logger.Error().Err(err).Str("token_url", env.OAuth.TokenURL).Msg("OAuth token refresh request failed")
		return false
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tokenResp.Body)
		s.logger.Error().Int("status", tokenResp.StatusCode).Str("body", string(body)).Msg("OAuth token refresh returned non-200")
		return false
	}
	var tokenData struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		s.logger.Error().Err(err).Msg("failed to decode OAuth token response")
		return false
	}
	if tokenData.AccessToken == "" {
		s.logger.Error().Msg("OAuth token response missing access_token")
		return false
	}
	wsName, secretName := parseSecretRef(env.SecretRef)
	resolver := secrets.GetResolver()
	if err := resolver.Set(wsName, secretName, []byte(strings.TrimSpace(tokenData.AccessToken))); err != nil {
		s.logger.Error().Err(err).Msg("failed to store refreshed OAuth access token")
		return false
	}
	resolver.FlushCache()
	s.logger.Info().Str("secret_ref", env.SecretRef).Msg("OAuth access token refreshed and stored")
	return true
}

func (s *Server) buildBody(template string, params map[string]interface{}) ([]byte, error) {
	result := template
	result = strings.Replace(result, "{{now}}", time.Now().UTC().Format("20060102150405"), -1)
	for key, val := range params {
		result = strings.Replace(result, "{{"+key+"}}", formatParamValue(val), -1)
	}
	for {
		start := strings.Index(result, "{{")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		result = result[:start] + result[start+end+2:]
	}
	return []byte(result), nil
}

// formatParamValue renders a param value as a string for template
// substitution. JSON unmarshalling turns every number into a float64, so an
// integer-valued field like 12345678 would render as "1.2345678e+07" under the
// default %v (%g) formatting and break int fields upstream. Integral floats are
// therefore formatted as plain integers, and non-integral floats use 'f'
// formatting to avoid scientific notation entirely.
func formatParamValue(val interface{}) string {
	switch v := val.(type) {
	case string:
		escaped, _ := json.Marshal(v)
		return string(escaped[1 : len(escaped)-1])
	case float64:
		if isIntegral(v) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		f := float64(v)
		if isIntegral(f) {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 32)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// isIntegral reports whether f is a finite whole number that fits in an int64,
// so it can be formatted as a plain integer without overflow.
func isIntegral(f float64) bool {
	return f == math.Trunc(f) && !math.IsInf(f, 0) &&
		f >= math.MinInt64 && f <= math.MaxInt64
}

func (s *Server) writeDeny(w http.ResponseWriter, r *http.Request, agentID, workspace, opID, rule, detail string) {
	requestID := uuid.New().String()
	s.auditLog.LogDeny(requestID, agentID, workspace, opID, rule, detail)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   "denied",
		"rule":    rule,
		"detail":  detail,
	})
}

func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	wsName := r.URL.Query().Get("workspace")

	out := []string{
		"=== external_gateway ===",
		"",
		"Endpoints:",
		"  GET /healthz                        liveness check",
		"  GET /help[?workspace=<name>]        this page",
		"  GET /whoami                         your identity + grants",
		"  GET /capabilities?workspace=<name>  operation schemas",
		"  POST /v1/workspaces/<w>/op/<id>     execute operation",
		"  POST /v1/workspaces/<w>/op/<id>?dry_run=true  validate only",
		"  POST /feedback                      report bug / request feature",
		"",
		"Auth:",
		"  Authorization: Bearer <token>",
		"  Get your token from the operator.",
		"",
	}

	if wsName != "" {
		ws := s.cfg().GetWorkspace(wsName)
		if ws == nil {
			http.Error(w, "workspace not found", http.StatusNotFound)
			return
		}
		out = append(out, s.workspaceHelp(wsName, ws)...)
	} else if tokenSHA := s.authenticate(r); tokenSHA != "" {
		agent := s.cfg().GetAgentByTokenSHA(tokenSHA)
		if agent != nil {
			seen := make(map[string]bool)
			for _, g := range agent.Grants {
				if !seen[g.Workspace] {
					seen[g.Workspace] = true
					ws := s.cfg().GetWorkspace(g.Workspace)
					if ws != nil {
						out = append(out, s.workspaceHelp(g.Workspace, ws)...)
					}
				}
			}
		}
	} else {
		out = append(out, "Feedback: POST /feedback  {\"type\":\"bug|feature\",\"text\":\"...\"}")
		for name := range s.cfg().Workspaces {
			out = append(out, "")
			out = append(out, fmt.Sprintf("Workspace: %s", name))
			ws := s.cfg().GetWorkspace(name)
			if ws != nil && ws.Description != "" {
				out = append(out, fmt.Sprintf("  %s", ws.Description))
			}
		}
		out = append(out, "")
		out = append(out, "For details, authenticate and hit /help, or use GET /help?workspace=<name>")
	}

	w.Write([]byte(strings.Join(out, "\r\n")))
}

func (s *Server) workspaceHelp(name string, ws *config.Workspace) []string {
	var out []string

	out = append(out, fmt.Sprintf("Workspace: %s", name))
	if ws.Description != "" {
		out = append(out, fmt.Sprintf("  %s", ws.Description))
	}
	out = append(out, "")

	// Sites and environments
	out = append(out, "Services:")
	for siteType, site := range ws.Sites {
		var envs []string
		for envName := range site.Environments {
			envs = append(envs, envName)
		}
		out = append(out, fmt.Sprintf("  %s: %s", siteType, strings.Join(envs, ", ")))
	}
	out = append(out, "")

	// PostgreSQL connectable databases per environment
	if site, ok := ws.Sites["postgres"]; ok {
		out = append(out, "PostgreSQL databases:")
		for envName, env := range site.Environments {
			var dbs []string
			for key := range env.Databases {
				dbs = append(dbs, key)
			}
			sort.Strings(dbs)
			out = append(out, fmt.Sprintf("  %s: %s", envName, strings.Join(dbs, ", ")))
		}
		out = append(out, "")
	}

	// Tunnels required by this workspace's environments
	tunnelRefs := make(map[string]bool)
	for _, site := range ws.Sites {
		for _, env := range site.Environments {
			if env.RequiresTunnel != "" {
				tunnelRefs[env.RequiresTunnel] = true
			}
		}
	}
	if len(tunnelRefs) > 0 {
		out = append(out, "Required tunnels:")
		for t := range tunnelRefs {
			if tun, ok := s.cfg().Tunnels[t]; ok {
				out = append(out, fmt.Sprintf("  %s (%s)", t, tun.Type))
			}
		}
		out = append(out, "")
	}

	// Operations available for this workspace
	out = append(out, "Operations:")
	wsOps := s.getWorkspaceOperations(ws)
	for id, op := range wsOps {
		required := ""
		if len(op.Params) > 0 {
			var req []string
			for n, p := range op.Params {
				if p.Required {
					req = append(req, n)
				}
			}
			if len(req) > 0 {
				required = "  params: " + strings.Join(req, ", ")
			}
		}
		method := op.Method
		if method == "" {
			method = "POST"
		}
		envHint := ""
		if op.Site != "" {
			if site, ok := ws.Sites[op.Site]; ok {
				var envNames []string
				for e := range site.Environments {
					envNames = append(envNames, e)
				}
				if len(envNames) > 0 {
					envHint = "  env: " + strings.Join(envNames, "|")
				}
			}
		}
		out = append(out, fmt.Sprintf("  %s", id))
		if required != "" {
			out = append(out, fmt.Sprintf("    %s", required))
		}
		if envHint != "" {
			out = append(out, fmt.Sprintf("    %s", envHint))
		}
		if method != "POST" {
			out = append(out, fmt.Sprintf("    method: %s", method))
		}
		if op.Path != "" {
			out = append(out, fmt.Sprintf("    path: %s", op.Path))
		}
	}
	out = append(out, "")

	// Usage examples per site type
	out = append(out, "Examples:")
	for siteType := range ws.Sites {
		switch siteType {
		case "jira":
			out = append(out, fmt.Sprintf("  %s.jira.search  POST {\"jql\":\"project=MD\",\"env\":\"prod\"}", name))
			out = append(out, fmt.Sprintf("  %s.jira.issue   POST {\"key\":\"MD-123\",\"env\":\"prod\"}", name))
			out = append(out, fmt.Sprintf("  %s.jira.create  POST {\"project\":\"MD\",\"summary\":\"...\",\"env\":\"prod\"}", name))
		case "bitbucket":
			out = append(out, fmt.Sprintf("  %s.bitbucket.pr  POST {\"repo\":\"my-repo\",\"pr_id\":1}", name))
		case "gitlab":
			out = append(out, fmt.Sprintf("  %s.gitlab.mr    POST {\"project_id\":\"group/repo\",\"mr_iid\":1}", name))
			out = append(out, fmt.Sprintf("  %s.gitlab.projects POST {\"search\":\"name\"}  (lists visible projects)", name))
		case "postgres":
			out = append(out, fmt.Sprintf("  %s.postgres.query  POST {\"env\":\"dev\",\"db\":\"mydb\",\"sql\":\"SELECT 1\"}", name))
			out = append(out, fmt.Sprintf("  %s.postgres.tables POST {\"env\":\"dev\",\"db\":\"mydb\"}", name))
		case "kibana":
			out = append(out, fmt.Sprintf("  %s.kibana.search POST {\"env\":\"dev\",\"query\":\"...\"}", name))
			out = append(out, fmt.Sprintf("  %s.kibana.login  POST {\"env\":\"dev\",\"password\":\"...\"}", name))
		case "clockify":
			out = append(out, fmt.Sprintf("  %s.clockify.entries POST {\"env\":\"prod\"}", name))
		}
	}
	out = append(out, "")

	return out
}

func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	body, _ := io.ReadAll(r.Body)
	if len(body) == 0 {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	var fb struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &fb); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if fb.Type == "" {
		fb.Type = "feedback"
	}

	entry := fmt.Sprintf("%s | %s | %s\r\n",
		time.Now().UTC().Format(time.RFC3339),
		fb.Type,
		strings.ReplaceAll(fb.Text, "\n", " "),
	)

	f, err := os.OpenFile("feedback.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		http.Error(w, "failed to write feedback", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	f.WriteString(entry)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "recorded"})
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	parts := strings.SplitN(path, "/", 2)

	if len(parts) == 0 || parts[0] == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"endpoints": "POST /admin/secrets, POST /admin/reload, POST /admin/diag",
		})
		return
	}

	switch parts[0] {
	case "secrets":
		if !s.authenticateAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodDelete {
			workspace := r.URL.Query().Get("workspace")
			name := r.URL.Query().Get("name")
			if workspace == "" || name == "" {
				http.Error(w, "workspace and name query params required", http.StatusBadRequest)
				return
			}
			resolver := secrets.GetResolver()
			if err := resolver.Delete(workspace, name); err != nil {
				http.Error(w, fmt.Sprintf("delete failed: %v", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
			return
		}
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			http.Error(w, "POST, PUT, or DELETE required", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return
		}
		var req struct {
			Workspace string `json:"workspace"`
			Name      string `json:"name"`
			Value     string `json:"value"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Workspace == "" || req.Name == "" || req.Value == "" {
			http.Error(w, "workspace, name, value required", http.StatusBadRequest)
			return
		}
		resolver := secrets.GetResolver()
		if err := resolver.Set(req.Workspace, req.Name, []byte(req.Value)); err != nil {
			http.Error(w, fmt.Sprintf("failed: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	case "diag":
		if !s.authenticateAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Workspace string `json:"workspace"`
			Site      string `json:"site"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil || req.Workspace == "" || req.Site == "" {
			http.Error(w, "workspace and site required", http.StatusBadRequest)
			return
		}
		cfg := s.cfg()
		ws, ok := cfg.Workspaces[req.Workspace]
		if !ok {
			http.Error(w, "workspace not found", http.StatusNotFound)
			return
		}
		site, ok := ws.Sites[req.Site]
		if !ok {
			http.Error(w, "site not found", http.StatusNotFound)
			return
		}
		results := map[string]interface{}{}
		for envName, env := range site.Environments {
			resolved := false
			if env.SecretRef != "" {
				wsName, sName := parseSecretRef(env.SecretRef)
				if _, err := secrets.ResolveWithWorkspaceCheck(wsName, sName, req.Workspace); err == nil {
					resolved = true
				}
			}
			envResult := map[string]interface{}{"secret_resolved": resolved}

			if resolved && env.User != "" {
				// Email / IMAP test
				host := env.Host
				if host == "" {
					host = env.ImapHost
				}
				if host != "" {
					emailCfg := &email.Config{ImapHost: host, ImapPort: 993, ImapTLS: true, User: env.User}
					if env.ImapPort != 0 {
						emailCfg.ImapPort = env.ImapPort
					}
					wsName, sName := parseSecretRef(env.SecretRef)
					secret, _ := secrets.ResolveWithWorkspaceCheck(wsName, sName, req.Workspace)
					emailCfg.Password = string(secret)
					if _, err := s.emailConn.ListMailboxes(emailCfg); err == nil {
						envResult["imap_login"] = "ok"
					} else {
						envResult["imap_login"] = "failed"
						envResult["imap_error"] = err.Error()
					}
				}

				// SMTP test
				smtpHost := env.SmtpHost
				if smtpHost == "" {
					smtpHost = env.Host
				}
				if smtpHost != "" {
					smtpCfg := &email.Config{SmtpHost: smtpHost, SmtpPort: 587, SmtpTLS: true, User: env.User}
					if env.SmtpPort != 0 {
						smtpCfg.SmtpPort = env.SmtpPort
					}
					wsName, sName := parseSecretRef(env.SecretRef)
					secret, _ := secrets.ResolveWithWorkspaceCheck(wsName, sName, req.Workspace)
					smtpCfg.Password = string(secret)
					// Just verify SMTP auth, don't actually send
					if _, err := s.emailConn.VerifySMTP(smtpCfg); err == nil {
						envResult["smtp_login"] = "ok"
					} else {
						envResult["smtp_login"] = "failed"
						envResult["smtp_error"] = err.Error()
					}
				}
			}

			if resolved && env.BaseURL != "" {
				headers := map[string]string{"Accept": "application/json"}
				if env.Auth.Type == "bearer" {
					wsName, sName := parseSecretRef(env.SecretRef)
					if secret, err := secrets.ResolveWithWorkspaceCheck(wsName, sName, req.Workspace); err == nil {
						headers["Authorization"] = "Bearer " + strings.TrimSpace(string(secret))
					}
				} else if env.Auth.Type == "basic" {
					wsName, sName := parseSecretRef(env.SecretRef)
					if secret, err := secrets.ResolveWithWorkspaceCheck(wsName, sName, req.Workspace); err == nil {
						headers["Authorization"] = "Basic " + base64Auth(env.User, strings.TrimSpace(string(secret)))
					}
				}
				if len(headers) > 1 {
					resp, err := s.httpClient.Execute(context.Background(), &connectors.Request{
						Method: "GET", Path: env.BaseURL, Headers: headers, Insecure: env.InsecureSkipVerify,
					})
					if err == nil && resp.StatusCode > 0 && resp.StatusCode < 500 {
						envResult["http_auth"] = "ok"
						envResult["http_status"] = resp.StatusCode
					} else {
						envResult["http_auth"] = "failed"
						if err != nil {
							envResult["http_error"] = err.Error()
						} else {
							envResult["http_status"] = resp.StatusCode
						}
					}
				}
			}

			results[envName] = envResult
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)

	case "reload":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if !s.authenticateAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cfg, err := config.Load(s.cfg().ConfigPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("config reload failed: %v", err), http.StatusInternalServerError)
			return
		}
		newReg, err := policy.NewRegistry(s.opsPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("ops reload failed: %v", err), http.StatusInternalServerError)
			return
		}
		checksum := computeChecksum(cfg.ConfigPath)
		s.cfgPtr.Store(cfg)
		s.reg = newReg
		s.configChecksum.Store(checksum)
		s.logger.Info().Str("checksum", checksum).Msg("config and ops hot-reloaded")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":          "ok",
			"config_checksum": checksum,
		})

	default:
		http.Error(w, "unknown admin endpoint", http.StatusNotFound)
	}
}

func (s *Server) authenticate(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	token := auth[7:]

	hash := sha256.Sum256([]byte(token))
	tokenSHA := hex.EncodeToString(hash[:])

	if s.cfg().GetAgentByTokenSHA(tokenSHA) != nil {
		return tokenSHA
	}

	for _, agent := range s.cfg().Agents {
		if agent.TokenSHA == tokenSHA {
			return tokenSHA
		}
	}

	return ""
}

func (s *Server) authenticateAdmin(r *http.Request) bool {
	if s.cfg().AdminTokenSHA == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:]) == s.cfg().AdminTokenSHA
}

func (s *Server) executeEmailConnector(ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error) {
	site, ok := ws.Sites[op.Site]
	if !ok {
		return 0, nil, fmt.Errorf("site %s not found", op.Site)
	}

	envName, _ := params["env"].(string)
	var env *config.Environment
	if envName != "" {
		if e, ok := site.Environments[envName]; ok {
			env = &e
		}
	}
	if env == nil {
		for _, e := range site.Environments {
			env = &e
			break
		}
	}
	if env == nil {
		return 0, nil, fmt.Errorf("no environment found for site %s", op.Site)
	}

	wsName, secretName := parseSecretRef(env.SecretRef)
	secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace)
	if err != nil {
		return 0, nil, fmt.Errorf("resolving email secret: %w", err)
	}

	imapHost := env.Host
	if imapHost == "" {
		imapHost = env.ImapHost
	}
	imapPort := env.ImapPort
	if imapPort == 0 {
		imapPort = 993
	}

	smtpHost := env.SmtpHost
	if smtpHost == "" {
		smtpHost = env.Host
	}
	smtpPort := env.SmtpPort
	if smtpPort == 0 {
		smtpPort = 587
	}

	emailCfg := &email.Config{
		ImapHost:   imapHost,
		ImapPort:   imapPort,
		ImapTLS:    true,
		SmtpHost:   smtpHost,
		SmtpPort:   smtpPort,
		SmtpTLS:    true,
		User:       env.User,
		Password:   string(secret),
	}

	var result interface{}

	// Email operations are named <site>.<action> (e.g. gmail.list_mailboxes, hotmail.send).
	// Extract the action part by taking everything after the first dot.
	action := op.ID
	if idx := strings.Index(op.ID, "."); idx != -1 && idx+1 < len(op.ID) {
		action = op.ID[idx+1:]
	}

	switch action {
	case "list_mailboxes":
		mboxes, err := s.emailConn.ListMailboxes(emailCfg)
		if err != nil {
			return 0, nil, err
		}
		mboxes = filterMailboxes(mboxes, ws.Policy.EmailAllowedMailboxes, ws.Policy.EmailDeniedMailboxes)
		result = map[string]interface{}{"mailboxes": mboxes}

	case "search":
		mailbox, _ := params["mailbox"].(string)
		if mailbox == "" {
			mailbox = "INBOX"
		}
		criteria, _ := params["criteria"].(string)

		// Deny access to blacklisted mailboxes
		if denyResource(mailbox, ws.Policy.EmailDeniedMailboxes, ws.Policy.EmailAllowedMailboxes) {
			return 0, nil, fmt.Errorf("mailbox %s denied by policy", mailbox)
		}

		messages, err := s.emailConn.Search(emailCfg, mailbox, criteria)
		if err != nil {
			return 0, nil, err
		}
		messages = filterMessagesByFrom(messages, ws.Policy.EmailAllowedSenders, ws.Policy.EmailDeniedSenders)
		result = map[string]interface{}{"messages": messages}

	case "read":
		mailbox, _ := params["mailbox"].(string)
		if mailbox == "" {
			mailbox = "INBOX"
		}
		if denyResource(mailbox, ws.Policy.EmailDeniedMailboxes, ws.Policy.EmailAllowedMailboxes) {
			return 0, nil, fmt.Errorf("mailbox %s denied by policy", mailbox)
		}

		uidFloat, ok := params["uid"].(float64)
		if !ok {
			return 0, nil, fmt.Errorf("uid parameter required")
		}
		msg, err := s.emailConn.ReadMessage(emailCfg, mailbox, uint32(uidFloat))
		if err != nil {
			return 0, nil, err
		}
		if denyMessageByFrom(msg, ws.Policy.EmailAllowedSenders, ws.Policy.EmailDeniedSenders) {
			return 0, nil, fmt.Errorf("message from %v denied by policy", msg["from"])
		}
		result = msg

	case "send":
		to, _ := params["to"].(string)
		subject, _ := params["subject"].(string)
		body, _ := params["body"].(string)
		if to == "" {
			return 0, nil, fmt.Errorf("to parameter required")
		}
		if denyResource(to, ws.Policy.EmailDeniedSenders, ws.Policy.EmailAllowedSenders) {
			return 0, nil, fmt.Errorf("recipient %s denied by policy", to)
		}
		if err := s.emailConn.Send(emailCfg, to, subject, body); err != nil {
			return 0, nil, err
		}
		result = map[string]string{"status": "sent", "to": to}

	default:
		return 0, nil, fmt.Errorf("unknown email operation: %s", op.ID)
	}

	body, _ := json.Marshal(result)
	return 200, body, nil
}

func matchPolicyPattern(value, pattern string) bool {
	if !strings.Contains(pattern, "*") && !strings.Contains(pattern, "?") {
		return strings.EqualFold(value, pattern)
	}
	ok, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(value))
	return err == nil && ok
}

func denyResource(value string, denied, allowed []string) bool {
	for _, pattern := range denied {
		if matchPolicyPattern(value, pattern) {
			return true
		}
	}
	if len(allowed) == 0 {
		return false
	}
	for _, pattern := range allowed {
		if matchPolicyPattern(value, pattern) {
			return false
		}
	}
	return true
}

func filterMailboxes(mboxes []map[string]interface{}, allowed, denied []string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, m := range mboxes {
		name, _ := m["name"].(string)
		if denyResource(name, denied, allowed) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func filterMessagesByFrom(msgs []map[string]interface{}, allowed, denied []string) []map[string]interface{} {
	if len(allowed) == 0 && len(denied) == 0 {
		return msgs
	}
	var out []map[string]interface{}
	for _, m := range msgs {
		from, _ := m["from"].(string)
		if denyResource(from, denied, allowed) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func denyMessageByFrom(msg map[string]interface{}, allowed, denied []string) bool {
	from, _ := msg["from"].(string)
	return denyResource(from, denied, allowed)
}
