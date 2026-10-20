package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/secrets"
	"github.com/external_gateway/internal/tokencheck"
)

type secretUsage struct {
	Site    string `json:"site"`
	Env     string `json:"env"`
	Role    string `json:"role"`
	User    string `json:"user,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Tunnel   string `json:"tunnel,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

type secretStatus struct {
	Name        string             `json:"name"`
	Role        string             `json:"role"`
	Kind        tokencheck.Kind    `json:"kind"`
	Pair        string             `json:"pair,omitempty"`
	Stored      bool               `json:"stored"`
	LastWritten string             `json:"last_written,omitempty"`
	UsedBy      []secretUsage      `json:"used_by"`
	Disabled    bool               `json:"disabled,omitempty"`
	Check       *tokencheck.Result `json:"check,omitempty"`
}

// collectSecrets lists every secret a workspace references, grouped by secret name,
// in a stable site/env order.
func collectSecrets(wsName string, ws *config.Workspace) []*secretStatus {
	byName := map[string]*secretStatus{}
	var order []string
	add := func(ref, role, pair string, site, envName string, env config.Environment) {
		if ref == "" {
			return
		}
		refWs, name := parseSecretRef(ref)
		if refWs != wsName {
			return
		}
		st, ok := byName[name]
		if !ok {
			st = &secretStatus{Name: name, Role: role, Kind: tokencheck.DetectKind(site, env)}
			byName[name] = st
			order = append(order, name)
		}
		if pair != "" {
			_, st.Pair = parseSecretRef(pair)
		}
		base := env.BaseURL
		if base == "" {
			base = env.Host
		}
		st.UsedBy = append(st.UsedBy, secretUsage{Site: site, Env: envName, Role: role, User: env.User, BaseURL: base, Tunnel: env.RequiresTunnel,
			Disabled: ws.Sites[site].Disabled})
	}

	sites := make([]string, 0, len(ws.Sites))
	for n := range ws.Sites {
		sites = append(sites, n)
	}
	sort.Strings(sites)
	for _, site := range sites {
		envs := make([]string, 0, len(ws.Sites[site].Environments))
		for n := range ws.Sites[site].Environments {
			envs = append(envs, n)
		}
		sort.Strings(envs)
		for _, envName := range envs {
			env := ws.Sites[site].Environments[envName]
			add(env.SecretRef, "primary", env.CookieSecretRef, site, envName, env)
			add(env.CookieSecretRef, "secondary", env.SecretRef, site, envName, env)
			if env.OAuth != nil {
				add(env.OAuth.ClientSecretRef, "oauth-client", "", site, envName, env)
				add(env.OAuth.RefreshSecretRef, "oauth-refresh", "", site, envName, env)
			}
		}
	}

	out := make([]*secretStatus, 0, len(order))
	for _, n := range order {
		st := byName[n]
		st.Disabled = true
		for _, u := range st.UsedBy {
			st.Disabled = st.Disabled && u.Disabled
		}
		out = append(out, st)
	}
	return out
}

func (s *Server) handleSecretStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	wsName := r.URL.Query().Get("workspace")
	ws, ok := s.cfg().Workspaces[wsName]
	if !ok {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	runChecks := r.URL.Query().Get("check") != "0"

	list := collectSecrets(wsName, &ws)
	resolver := secrets.GetResolver()
	values := map[string]string{}
	for _, st := range list {
		if v, err := secrets.ResolveWithWorkspaceCheck(wsName, st.Name, wsName); err == nil {
			st.Stored = true
			values[st.Name] = string(v)
		}
		if t, ok := resolver.LastWritten(wsName, st.Name); ok {
			st.LastWritten = t.UTC().Format(time.RFC3339)
		}
	}

	if runChecks {
		var wg sync.WaitGroup
		for _, st := range list {
			if st.Role != "primary" || !st.Stored || st.Disabled {
				continue
			}
			wg.Add(1)
			go func(st *secretStatus) {
				defer wg.Done()
				st.Check = s.checkSecret(wsName, &ws, st, values)
			}(st)
		}
		wg.Wait()
		for _, st := range list {
			if st.Role == "secondary" && st.Pair != "" {
				for _, p := range list {
					if p.Name == st.Pair && p.Check != nil {
						st.Check = p.Check
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"workspace": wsName, "secrets": list})
}

func (s *Server) checkSecret(wsName string, ws *config.Workspace, st *secretStatus, values map[string]string) *tokencheck.Result {
	for _, u := range st.UsedBy {
		if u.Disabled {
			continue
		}
		env := ws.Sites[u.Site].Environments[u.Env]
		kind := tokencheck.DetectKind(u.Site, env)
		if !tokencheck.Checkable(kind) {
			continue
		}
		secondary := ""
		if env.CookieSecretRef != "" {
			_, n := parseSecretRef(env.CookieSecretRef)
			secondary = values[n]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		res := tokencheck.Check(ctx, kind, env, values[st.Name], secondary)
		cancel()
		if res.Detail == "" {
			res.Detail = "checked via " + u.Site + "/" + u.Env
		} else {
			res.Detail += " (checked via " + u.Site + "/" + u.Env + ")"
		}
		return &res
	}
	return &tokencheck.Result{State: tokencheck.StateUnchecked, Detail: "no automatic check for this credential type"}
}
