//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/external_gateway/internal/tokencheck"
)

type wsResult struct {
	Info      workspaceInfo `json:"info"`
	Problem   string        `json:"problem,omitempty"`
	Detail    string        `json:"detail,omitempty"`
	Secrets   []secretRow   `json:"secrets,omitempty"`
	CheckedAt time.Time     `json:"checked_at"`
	Live      bool          `json:"live"`
}

type todoAction struct {
	Kind   string `json:"kind"`
	WS     string `json:"ws,omitempty"`
	Secret string `json:"secret,omitempty"`
	Arg    string `json:"arg,omitempty"`
}

type todo struct {
	Key      string     `json:"key"`
	WS       string     `json:"ws"`
	Severity string     `json:"severity"`
	Title    string     `json:"title"`
	Detail   string     `json:"detail,omitempty"`
	Action   todoAction `json:"action"`
	Notify   bool       `json:"-"`
}

// fetchStatus asks the workspace's gateway for its secret status; live runs the upstream checks.
func (a *app) fetchStatus(ws string, live bool) *wsResult {
	in, cfg, err := a.info(ws)
	res := &wsResult{Info: in, CheckedAt: time.Now(), Live: live}
	if err != nil {
		res.Problem, res.Detail = "config_error", err.Error()
		return res
	}
	key := workspaceKey(ws, cfg)
	path := "/admin/secrets/status?workspace=" + key
	if !live {
		path += "&check=0"
	}
	status, data, err := a.gateway(ws, "GET", path, nil)
	switch {
	case err != nil:
		res.Problem, res.Detail = "gateway_down", err.Error()
		return res
	case status == 401:
		res.Problem = "admin_unauthorized"
		return res
	case status == 405 || (status == 404 && strings.Contains(string(data), "unknown admin endpoint")):
		res.Problem = "gateway_outdated"
		return res
	case status != 200:
		res.Problem, res.Detail = "gateway_error", fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(data)))
		return res
	}
	var out struct {
		Secrets []secretRow `json:"secrets"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		res.Problem, res.Detail = "gateway_error", err.Error()
		return res
	}
	sites := cfg.Workspaces[key].Sites
	for i := range out.Secrets {
		s := &out.Secrets[i]
		allDisabled := len(s.UsedBy) > 0
		for j := range s.UsedBy {
			s.UsedBy[j].Disabled = sites[s.UsedBy[j].Site].Disabled
			allDisabled = allDisabled && s.UsedBy[j].Disabled
		}
		if allDisabled && !s.Disabled {
			s.Disabled, s.Check = true, nil
		}
		var names []string
		for _, u := range s.UsedBy {
			names = append(names, u.Site+"/"+u.Env)
		}
		first := usage{}
		if len(s.UsedBy) > 0 {
			first = s.UsedBy[0]
		}
		s.Help = helpFor(helpInput{Kind: s.Kind, Role: s.Role, Name: s.Name, Pair: s.Pair, Workspace: key, Port: in.Port,
			BuildDir: a.buildDir, User: first.User, BaseURL: first.BaseURL, Tunnel: first.Tunnel, UsedBy: names})
	}
	res.Secrets = out.Secrets
	return res
}

func (a *app) refresh(ws string, live bool) *wsResult {
	res := a.fetchStatus(ws, live)
	a.mu.Lock()
	if prev, ok := a.results[ws]; ok && !live && prev.Live && res.Problem == "" {
		checks := map[string]*tokencheck.Result{}
		for _, s := range prev.Secrets {
			checks[s.Name] = s.Check
		}
		for i := range res.Secrets {
			res.Secrets[i].Check = checks[res.Secrets[i].Name]
		}
		res.Live = true
		res.CheckedAt = prev.CheckedAt
	}
	a.results[ws] = res
	a.mu.Unlock()
	a.changed()
	return res
}

func (a *app) checkAll() {
	a.mu.Lock()
	if a.checking {
		a.mu.Unlock()
		return
	}
	a.checking = true
	a.mu.Unlock()
	a.changed()

	var wg sync.WaitGroup
	for _, ws := range a.workspaceNames() {
		wg.Add(1)
		go func(ws string) {
			defer wg.Done()
			a.refresh(ws, true)
		}(ws)
	}
	wg.Wait()

	a.mu.Lock()
	a.checking = false
	a.lastCheck = time.Now()
	a.mu.Unlock()
	a.notifyNew()
	a.changed()
}

func (a *app) checkLoop() {
	time.Sleep(15 * time.Second)
	for {
		a.checkAll()
		a.mu.Lock()
		interval := time.Duration(a.settings.CheckIntervalMin) * time.Minute
		a.mu.Unlock()
		time.Sleep(interval)
	}
}

func (a *app) changed() {
	if a.onChange != nil {
		a.onChange()
	}
}

func daysUntil(date string) (int, bool) {
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, date); err == nil {
			if layout == "2006-01-02" {
				t = t.Add(24*time.Hour - time.Second)
			}
			return int(time.Until(t).Hours() / 24), true
		}
	}
	return 0, false
}

func (a *app) todos() []todo {
	a.mu.Lock()
	results := make(map[string]*wsResult, len(a.results))
	for k, v := range a.results {
		results[k] = v
	}
	warnDays := a.settings.ExpiryWarnDays
	a.mu.Unlock()

	var out []todo
	add := func(t todo) { out = append(out, t) }

	for _, ws := range a.workspaceNames() {
		r := results[ws]
		if r == nil {
			continue
		}
		in := r.Info
		switch {
		case in.ServiceInstalled && in.ServiceState != "running":
			add(todo{Key: ws + "/service", WS: ws, Severity: "bad", Notify: true, Title: "Gateway service " + in.Service + " is " + in.ServiceState,
				Action: todoAction{Kind: "service", WS: ws, Arg: "start"}})
		case !in.GatewayUp && in.ServiceInstalled:
			add(todo{Key: ws + "/down", WS: ws, Severity: "bad", Notify: true, Title: fmt.Sprintf("Gateway not answering on port %d", in.Port),
				Action: todoAction{Kind: "service", WS: ws, Arg: "restart"}})
		}
		if !in.AdminConfigured {
			add(todo{Key: ws + "/admin-locked", WS: ws, Severity: "warn", Title: "Admin API locked - secrets cannot be checked or changed",
				Detail: "No admin_token_sha256 in the config.", Action: todoAction{Kind: "enable-admin", WS: ws}})
		} else if !in.AdminTokenOK {
			add(todo{Key: ws + "/admin-token", WS: ws, Severity: "warn", Title: "Admin token not available",
				Detail: "No matching [" + strings.ToUpper(ws) + " Admin] entry in agent-keys.txt.", Action: todoAction{Kind: "open", WS: ws}})
		}
		if in.ConfigDrift {
			add(todo{Key: ws + "/drift", WS: ws, Severity: "warn", Title: "Config changes not published to the running gateway",
				Detail: "The source config or operations differ from the deployed copy.", Action: todoAction{Kind: "publish", WS: ws}})
		}
		switch r.Problem {
		case "gateway_outdated":
			add(todo{Key: ws + "/outdated", WS: ws, Severity: "warn", Title: "Running gateway build has no secret status endpoint",
				Action: todoAction{Kind: "deploy"}})
		case "gateway_error", "config_error":
			add(todo{Key: ws + "/error", WS: ws, Severity: "bad", Notify: true, Title: "Gateway error", Detail: r.Detail, Action: todoAction{Kind: "open", WS: ws}})
		case "admin_unauthorized":
			if in.AdminTokenOK {
				add(todo{Key: ws + "/admin-stale", WS: ws, Severity: "warn", Title: "Gateway rejected the admin token",
					Detail: "The running gateway has not loaded the current config.", Action: todoAction{Kind: "publish", WS: ws, Arg: "restart"}})
			}
		}
		for _, s := range r.Secrets {
			if (s.Role == "secondary" && s.Pair != "") || s.Disabled {
				continue
			}
			id := ws + "/secret/" + s.Name
			act := todoAction{Kind: "secret", WS: ws, Secret: s.Name}
			if !s.Stored {
				add(todo{Key: id + "/missing", WS: ws, Severity: "bad", Notify: true, Title: "Secret " + s.Name + " is not set", Detail: s.Help.Title, Action: act})
				continue
			}
			c := s.Check
			if c == nil {
				continue
			}
			switch c.State {
			case tokencheck.StateRejected:
				add(todo{Key: id + "/rejected", WS: ws, Severity: "bad", Notify: true, Title: "Secret " + s.Name + " rejected", Detail: c.Detail, Action: act})
			case tokencheck.StateUnreachable:
				add(todo{Key: id + "/unreachable", WS: ws, Severity: "info", Title: "Secret " + s.Name + " could not be checked", Detail: c.Detail, Action: act})
			case tokencheck.StateOK:
				if c.ExpiresAt == "" {
					continue
				}
				if n, ok := daysUntil(c.ExpiresAt); ok {
					if n < 0 {
						add(todo{Key: id + "/expired", WS: ws, Severity: "bad", Notify: true, Title: "Secret " + s.Name + " expired", Detail: "expired " + c.ExpiresAt, Action: act})
					} else if n <= warnDays {
						add(todo{Key: id + fmt.Sprintf("/expiring-%d", bucket(n)), WS: ws, Severity: "warn", Notify: true,
							Title: fmt.Sprintf("Secret %s expires in %d days", s.Name, n), Detail: "expires " + c.ExpiresAt, Action: act})
					}
				}
			}
		}
	}

	built := filepath.Join(a.buildDir, "egw.exe")
	installed := filepath.Join(a.installDir, "egw.exe")
	if b, i := fileSHA(built), fileSHA(installed); b != "" && i != "" && b != i {
		var disabledSites []string
		for _, ws := range a.workspaceNames() {
			if cfg, err := a.effectiveConfig(ws); err == nil {
				for _, site := range sortedKeys(cfg.Workspaces[workspaceKey(ws, cfg)].Sites) {
					if cfg.Workspaces[workspaceKey(ws, cfg)].Sites[site].Disabled {
						disabledSites = append(disabledSites, ws+"/"+site)
					}
				}
			}
		}
		t := todo{Key: "binary", Severity: "warn", Title: "A newer egw.exe build is not deployed",
			Detail: built + " differs from " + installed, Action: todoAction{Kind: "deploy"}}
		if len(disabledSites) > 0 {
			t.Title = "Disabled integrations are still active - deploy the new egw.exe"
			t.Detail = "The running gateway build ignores `disabled: true` (" + strings.Join(disabledSites, ", ") + ")."
			t.Severity, t.Notify, t.Key = "bad", true, "binary-disabled"
		}
		add(t)
	}

	rank := map[string]int{"bad": 0, "warn": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	return out
}

func bucket(days int) int {
	switch {
	case days <= 1:
		return 1
	case days <= 3:
		return 3
	case days <= 7:
		return 7
	}
	return 14
}

// notifyNew raises one toast for problems that appeared since the last notification.
func (a *app) notifyNew() {
	list := a.todos()
	a.mu.Lock()
	enabled := a.settings.Notify
	current := map[string]bool{}
	var fresh []todo
	for _, t := range list {
		if !t.Notify {
			continue
		}
		current[t.Key] = true
		if !a.notified[t.Key] {
			fresh = append(fresh, t)
		}
	}
	a.notified = current
	a.saveState()
	a.mu.Unlock()

	if !enabled || len(fresh) == 0 {
		return
	}
	title := fresh[0].WS + ": " + fresh[0].Title
	body := fresh[0].Detail
	if len(fresh) > 1 {
		title = fmt.Sprintf("External Gateway: %d new problems", len(fresh))
		var lines []string
		for i, t := range fresh {
			if i == 3 {
				lines = append(lines, fmt.Sprintf("and %d more", len(fresh)-3))
				break
			}
			lines = append(lines, t.WS+": "+t.Title)
		}
		body = strings.Join(lines, "\n")
	}
	showToast(title, body)
}

func (a *app) worstSeverity() (string, int) {
	list := a.todos()
	worst := ""
	count := 0
	for _, t := range list {
		if t.Severity == "info" {
			continue
		}
		count++
		if t.Severity == "bad" || worst == "" {
			worst = t.Severity
		}
	}
	return worst, count
}
