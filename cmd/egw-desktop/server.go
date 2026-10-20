//go:build windows

package main

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed ui.html
var uiHTML []byte

type handler func(*http.Request) (interface{}, error)

func (a *app) serve(l net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("s")), []byte(a.session)) != 1 {
			http.Error(w, "open the app from the External Gateway tray icon", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(uiHTML)
	})
	api := map[string]handler{
		"/api/overview":           a.apiOverview,
		"/api/check":              a.apiCheck,
		"/api/status":             a.apiStatus,
		"/api/test":               a.apiTest,
		"/api/save":               a.apiSave,
		"/api/admin-token":        a.apiAdminToken,
		"/api/admin-regenerate":   a.apiAdminRegenerate,
		"/api/service":            a.apiService,
		"/api/deploy":             a.apiDeploy,
		"/api/publish":            a.apiPublish,
		"/api/capabilities":       a.apiCapabilities,
		"/api/capability/add":     a.apiCapabilityAdd,
		"/api/capability/grants":  a.apiCapabilityGrants,
		"/api/capability/remove":  a.apiCapabilityRemove,
		"/api/service/add":        a.apiServiceAdd,
		"/api/service/disable":    a.apiServiceDisable,
		"/api/agents":             a.apiAgents,
		"/api/agent/regenerate":   a.apiAgentRegenerate,
		"/api/agent/add":          a.apiAgentAdd,
		"/api/settings":           a.apiSettings,
		"/api/open-window": func(*http.Request) (interface{}, error) {
			go a.openUI()
			return ok(), nil
		},
	}
	for path, fn := range api {
		fn := fn
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Session")), []byte(a.session)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			out, err := fn(r)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(out)
		})
	}
	host := fmt.Sprintf("127.0.0.1:%d", a.port)
	return http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

func ok() map[string]bool { return map[string]bool{"ok": true} }

func decode(r *http.Request, v interface{}) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("POST required")
	}
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func (a *app) apiOverview(*http.Request) (interface{}, error) {
	var infos []workspaceInfo
	for _, ws := range a.workspaceNames() {
		a.mu.Lock()
		r := a.results[ws]
		a.mu.Unlock()
		if r != nil {
			infos = append(infos, r.Info)
		} else {
			in, _, _ := a.info(ws)
			infos = append(infos, in)
		}
	}
	a.mu.Lock()
	last := a.lastCheck
	checking := a.checking
	st := a.settings
	a.mu.Unlock()
	lastText := ""
	if !last.IsZero() {
		lastText = last.Format(time.RFC3339)
	}
	return map[string]interface{}{
		"workspaces": infos, "todos": a.todos(), "last_check": lastText, "checking": checking,
		"elevated": a.elevated, "build_dir": a.buildDir, "install_dir": a.installDir, "data_dir": a.dataDir,
		"settings": st, "autostart": autostartEnabled(),
	}, nil
}

func (a *app) apiCheck(r *http.Request) (interface{}, error) {
	go a.checkAll()
	return ok(), nil
}

func (a *app) apiStatus(r *http.Request) (interface{}, error) {
	ws := r.URL.Query().Get("ws")
	if !validName.MatchString(ws) {
		return nil, fmt.Errorf("invalid workspace")
	}
	live := r.URL.Query().Get("check") != "0"
	if r.URL.Query().Get("cached") == "1" {
		a.mu.Lock()
		res := a.results[ws]
		a.mu.Unlock()
		if res != nil {
			return res, nil
		}
		live = false
	}
	return a.refresh(ws, live), nil
}

func (a *app) apiTest(r *http.Request) (interface{}, error) {
	var req secretRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return a.testSecret(req)
}

func (a *app) apiSave(r *http.Request) (interface{}, error) {
	var req secretRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := a.saveSecret(req); err != nil {
		return nil, err
	}
	go func() {
		a.refresh(req.WS, true)
		a.notifyNew()
	}()
	return ok(), nil
}

func (a *app) apiAdminToken(r *http.Request) (interface{}, error) {
	var req struct {
		WS       string `json:"ws"`
		Token    string `json:"token"`
		Remember bool   `json:"remember"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := a.useAdminToken(req.WS, req.Token, req.Remember); err != nil {
		return nil, err
	}
	go a.refresh(req.WS, true)
	return ok(), nil
}

// apiAdminRegenerate creates a new admin token, writes its hash, publishes and restarts the gateway.
func (a *app) apiAdminRegenerate(r *http.Request) (interface{}, error) {
	var req struct {
		WS string `json:"ws"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if _, err := a.setAdminToken(req.WS); err != nil {
		return nil, err
	}
	if _, err := a.runElevated("publish", req.WS, "", true); err != nil {
		return nil, fmt.Errorf("the new admin token is saved to the source config and agent-keys.txt, but publishing failed: %w", err)
	}
	go a.refresh(req.WS, true)
	return map[string]string{"ok": "admin token stored in agent-keys.txt as " + adminLabel(req.WS)}, nil
}

func (a *app) apiService(r *http.Request) (interface{}, error) {
	var req struct {
		WS     string `json:"ws"`
		Action string `json:"action"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if _, err := a.runElevated("service", req.WS, req.Action, false); err != nil {
		return nil, err
	}
	go a.refresh(req.WS, true)
	return ok(), nil
}

func (a *app) apiDeploy(r *http.Request) (interface{}, error) {
	var req struct{}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	res, err := a.runElevated("deploy", "", "", false)
	logText := time.Now().Format(time.RFC3339) + "\r\n" + res.Output
	if err != nil {
		logText += "\r\nERROR: " + err.Error()
	}
	os.WriteFile(filepath.Join(a.dataDir, "last-deploy.log"), []byte(logText), 0600)
	if err != nil {
		return map[string]string{"output": res.Output, "error": err.Error()}, nil
	}
	go a.checkAll()
	return map[string]string{"output": res.Output}, nil
}

// apiPublish copies the source config to the install dir (elevated) and reloads the gateway, or
// restarts it when asked or when the reload is refused.
func (a *app) apiPublish(r *http.Request) (interface{}, error) {
	var req struct {
		WS      string `json:"ws"`
		Restart bool   `json:"restart"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if _, err := a.runElevated("publish", req.WS, "", req.Restart); err != nil {
		return nil, err
	}
	how := "restarted"
	if !req.Restart {
		how = "reloaded"
		if err := a.reload(req.WS); err != nil {
			if _, err2 := a.runElevated("service", req.WS, "restart", false); err2 != nil {
				return nil, fmt.Errorf("published, but reload failed (%v) and restart failed (%v)", err, err2)
			}
			how = "restarted (reload was refused: " + err.Error() + ")"
		}
	}
	go a.refresh(req.WS, true)
	return map[string]string{"ok": "published and " + how}, nil
}

func (a *app) apiCapabilities(r *http.Request) (interface{}, error) {
	return a.capabilities(r.URL.Query().Get("ws"))
}

func editResult(changes []fileChange, applied bool) map[string]interface{} {
	return map[string]interface{}{"changes": changes, "applied": applied}
}

func (a *app) apiCapabilityAdd(r *http.Request) (interface{}, error) {
	var req addCapabilityRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	e, err := a.openEdit(req.WS)
	if err != nil {
		return nil, err
	}
	id := req.ID
	if err := e.addOperation(id, req.Source, "", req.Custom); err != nil {
		return nil, err
	}
	if len(req.Roles) > 0 {
		if err := e.addGrants(id, req.Roles); err != nil {
			return nil, err
		}
	}
	changes, err := e.commit(req.Apply)
	if err != nil {
		return nil, err
	}
	return editResult(changes, req.Apply), nil
}

func (a *app) apiCapabilityGrants(r *http.Request) (interface{}, error) {
	var req struct {
		WS    string   `json:"ws"`
		ID    string   `json:"id"`
		Roles []string `json:"roles"`
		Apply bool     `json:"apply"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	e, err := a.openEdit(req.WS)
	if err != nil {
		return nil, err
	}
	if err := e.setGrants(req.ID, req.Roles); err != nil {
		return nil, err
	}
	changes, err := e.commit(req.Apply)
	if err != nil {
		return nil, err
	}
	return editResult(changes, req.Apply), nil
}

func (a *app) apiCapabilityRemove(r *http.Request) (interface{}, error) {
	var req struct {
		WS    string `json:"ws"`
		ID    string `json:"id"`
		Apply bool   `json:"apply"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	e, err := a.openEdit(req.WS)
	if err != nil {
		return nil, err
	}
	if err := e.removeOperation(req.ID); err != nil {
		return nil, err
	}
	changes, err := e.commit(req.Apply)
	if err != nil {
		return nil, err
	}
	return editResult(changes, req.Apply), nil
}

func (a *app) apiServiceAdd(r *http.Request) (interface{}, error) {
	var req addServiceRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.Site = strings.TrimSpace(req.Site)
	req.Env = strings.TrimSpace(req.Env)
	e, err := a.openEdit(req.WS)
	if err != nil {
		return nil, err
	}
	if err := e.addService(req); err != nil {
		return nil, err
	}
	changes, err := e.commit(req.Apply)
	if err != nil {
		return nil, err
	}
	return editResult(changes, req.Apply), nil
}

func (a *app) apiAgents(r *http.Request) (interface{}, error) {
	list, roles, err := a.agents(r.URL.Query().Get("ws"))
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"agents": list, "roles": roles}, nil
}

func (a *app) apiAgentRegenerate(r *http.Request) (interface{}, error) {
	var req struct {
		WS string `json:"ws"`
		ID string `json:"id"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	token, err := a.regenerateAgent(req.WS, req.ID)
	if err != nil && token == "" {
		return nil, err
	}
	out := map[string]string{"token": token}
	if err != nil {
		out["warning"] = err.Error()
	}
	return out, nil
}

func (a *app) apiAgentAdd(r *http.Request) (interface{}, error) {
	var req struct {
		WS   string `json:"ws"`
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	token, err := a.addAgent(req.WS, req.ID, req.Role)
	if err != nil && token == "" {
		return nil, err
	}
	out := map[string]string{"token": token}
	if err != nil {
		out["warning"] = err.Error()
	}
	return out, nil
}

func (a *app) apiSettings(r *http.Request) (interface{}, error) {
	var req struct {
		CheckIntervalMin int   `json:"check_interval_min"`
		Notify           bool  `json:"notify"`
		ExpiryWarnDays   int   `json:"expiry_warn_days"`
		Autostart        *bool `json:"autostart"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.CheckIntervalMin < 5 || req.CheckIntervalMin > 1440 {
		return nil, fmt.Errorf("check interval must be 5-1440 minutes")
	}
	if req.ExpiryWarnDays < 1 || req.ExpiryWarnDays > 120 {
		return nil, fmt.Errorf("expiry warning must be 1-120 days")
	}
	a.mu.Lock()
	a.settings = settings{CheckIntervalMin: req.CheckIntervalMin, Notify: req.Notify, ExpiryWarnDays: req.ExpiryWarnDays}
	err := a.saveSettings()
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if req.Autostart != nil && *req.Autostart != autostartEnabled() {
		if err := setAutostart(*req.Autostart); err != nil {
			return nil, err
		}
	}
	a.changed()
	return ok(), nil
}

func (a *app) apiServiceDisable(r *http.Request) (interface{}, error) {
	var req struct {
		WS       string `json:"ws"`
		Site     string `json:"site"`
		Disabled bool   `json:"disabled"`
		Apply    bool   `json:"apply"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	e, err := a.openEdit(req.WS)
	if err != nil {
		return nil, err
	}
	if err := e.setSiteDisabled(req.Site, req.Disabled); err != nil {
		return nil, err
	}
	changes, err := e.commit(req.Apply)
	if err != nil {
		return nil, err
	}
	return editResult(changes, req.Apply), nil
}
