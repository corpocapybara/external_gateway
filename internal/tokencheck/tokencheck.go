// Package tokencheck verifies a stored or candidate credential against its upstream
// service with a cheap identity call, and classifies the outcome.
package tokencheck

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/email"
	"github.com/external_gateway/internal/connectors/postgres"
)

type State string

const (
	StateOK          State = "ok"
	StateRejected    State = "rejected"
	StateUnreachable State = "unreachable"
	StateUnchecked   State = "unchecked"
)

type Kind string

const (
	KindGitLab    Kind = "gitlab"
	KindGitHub    Kind = "github"
	KindAtlassian Kind = "atlassian"
	KindJira      Kind = "jira-server"
	KindJenkins   Kind = "jenkins"
	KindBitbucket Kind = "bitbucket"
	KindKibana    Kind = "kibana"
	KindClockify  Kind = "clockify"
	KindAuthentik Kind = "authentik"
	KindSlack     Kind = "slack-session"
	KindTrello    Kind = "trello"
	KindPostgres  Kind = "postgres"
	KindIMAP      Kind = "imap"
	KindOAuth     Kind = "oauth"
	KindMCP       Kind = "atlassian-mcp"
	KindGeneric   Kind = "generic"
)

type Result struct {
	State      State    `json:"state"`
	Detail     string   `json:"detail,omitempty"`
	Identity   string   `json:"identity,omitempty"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
	HTTPStatus int      `json:"http_status,omitempty"`
}

// DetectKind maps a configured site environment to the credential type it uses.
func DetectKind(site string, env config.Environment) Kind {
	base := strings.ToLower(env.BaseURL)
	switch {
	case env.OAuth != nil:
		return KindOAuth
	case env.Auth.Type == "slack-session":
		return KindSlack
	case env.Auth.Type == "trello":
		return KindTrello
	case env.Auth.Type == "api-key" && strings.Contains(base, "clockify"):
		return KindClockify
	case strings.Contains(base, "mcp.atlassian.com"):
		return KindMCP
	case strings.Contains(base, "atlassian.net"):
		return KindAtlassian
	case strings.Contains(base, "gitlab"):
		return KindGitLab
	case strings.Contains(base, "api.github.com"):
		return KindGitHub
	case strings.Contains(base, "api.bitbucket.org"):
		return KindBitbucket
	case site == "postgres" || len(env.Databases) > 0:
		return KindPostgres
	case site == "gmail" || site == "email" || env.ImapHost != "" || (env.BaseURL == "" && strings.HasPrefix(env.Host, "imap.")):
		return KindIMAP
	case site == "jenkins":
		return KindJenkins
	case site == "kibana":
		return KindKibana
	case site == "authentik":
		return KindAuthentik
	case site == "jira":
		return KindJira
	}
	return KindGeneric
}

// Checkable reports whether Check can verify credentials of this kind.
func Checkable(kind Kind) bool {
	switch kind {
	case KindOAuth, KindMCP, KindGeneric:
		return false
	}
	return true
}

// Check verifies primary (and, for paired credentials, secondary) against the upstream.
func Check(ctx context.Context, kind Kind, env config.Environment, primary, secondary string) Result {
	primary = strings.TrimSpace(primary)
	secondary = strings.TrimSpace(secondary)
	if primary == "" {
		return Result{State: StateUnchecked, Detail: "no value"}
	}
	switch kind {
	case KindGitLab:
		return checkGitLab(ctx, env, primary)
	case KindGitHub:
		return checkGitHub(ctx, env, primary)
	case KindAtlassian:
		root := strings.TrimSuffix(strings.TrimRight(env.BaseURL, "/"), "/wiki")
		return checkJSON(ctx, env, "GET", root+"/rest/api/3/myself", basic(env.User, primary), "displayName", "emailAddress")
	case KindJira:
		return checkJSON(ctx, env, "GET", strings.TrimRight(env.BaseURL, "/")+"/rest/api/2/myself", basic(env.User, primary), "displayName", "name")
	case KindJenkins:
		return checkJSON(ctx, env, "GET", strings.TrimRight(env.BaseURL, "/")+"/me/api/json", basic(env.User, primary), "fullName", "id")
	case KindBitbucket:
		return checkJSON(ctx, env, "GET", strings.TrimRight(env.BaseURL, "/")+"/2.0/user", basic(env.User, primary), "display_name", "username")
	case KindKibana:
		if env.BaseURL == "" {
			return Result{State: StateUnchecked, Detail: "no base_url configured"}
		}
		return checkJSON(ctx, env, "GET", strings.TrimRight(env.BaseURL, "/")+"/api/status", basic(env.User, primary), "name")
	case KindClockify:
		header := env.Auth.Header
		if header == "" {
			header = "X-Api-Key"
		}
		return checkJSON(ctx, env, "GET", strings.TrimRight(env.BaseURL, "/")+"/user", map[string]string{header: primary}, "name", "email")
	case KindAuthentik:
		return checkJSON(ctx, env, "GET", strings.TrimRight(env.BaseURL, "/")+"/api/v3/core/users/me/", bearer(primary), "user.username", "user.email")
	case KindSlack:
		return checkSlack(ctx, env, primary, secondary)
	case KindTrello:
		if secondary == "" {
			return Result{State: StateUnchecked, Detail: "needs both the API key and the token"}
		}
		u := strings.TrimRight(env.BaseURL, "/") + "/members/me?key=" + url.QueryEscape(primary) + "&token=" + url.QueryEscape(secondary)
		return checkJSON(ctx, env, "GET", u, nil, "username", "fullName")
	case KindPostgres:
		return checkPostgres(env, primary)
	case KindIMAP:
		return checkIMAP(env, primary)
	}
	return Result{State: StateUnchecked, Detail: "no automatic check for this credential type"}
}

func basic(user, secret string) map[string]string {
	req, _ := http.NewRequest("GET", "http://x", nil)
	req.SetBasicAuth(user, secret)
	return map[string]string{"Authorization": req.Header.Get("Authorization")}
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func client(env config.Environment) *http.Client {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: env.InsecureSkipVerify}}
	if env.ProxyHost != "" {
		tr.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", env.ProxyHost, env.ProxyPort)})
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func do(ctx context.Context, env config.Environment, method, u string, headers map[string]string, body io.Reader) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client(env).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return &response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

func unreachable(env config.Environment, err error) Result {
	detail := err.Error()
	if env.RequiresTunnel != "" {
		detail += fmt.Sprintf(" (needs tunnel %q up)", env.RequiresTunnel)
	}
	return Result{State: StateUnreachable, Detail: detail}
}

func classify(resp *response) Result {
	switch {
	case resp.status == 401 || resp.status == 403:
		return Result{State: StateRejected, HTTPStatus: resp.status, Detail: upstreamMessage(resp.body)}
	case resp.status >= 200 && resp.status < 300:
		return Result{State: StateOK, HTTPStatus: resp.status}
	case resp.status >= 500:
		return Result{State: StateUnreachable, HTTPStatus: resp.status, Detail: upstreamMessage(resp.body)}
	}
	return Result{State: StateRejected, HTTPStatus: resp.status, Detail: upstreamMessage(resp.body)}
}

func upstreamMessage(body []byte) string {
	var m map[string]interface{}
	if json.Unmarshal(body, &m) == nil {
		for _, k := range []string{"error_description", "message", "error", "errorMessages"} {
			if v, ok := m[k]; ok {
				return strings.TrimSpace(fmt.Sprint(v))
			}
		}
	}
	s := strings.TrimSpace(string(body))
	if strings.HasPrefix(s, "<") {
		return "HTML page returned (login wall or proxy?)"
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func lookup(m map[string]interface{}, path string) string {
	var cur interface{} = m
	for _, p := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]interface{})
		if !ok {
			return ""
		}
		cur = obj[p]
	}
	if cur == nil {
		return ""
	}
	return fmt.Sprint(cur)
}

func checkJSON(ctx context.Context, env config.Environment, method, u string, headers map[string]string, identityFields ...string) Result {
	resp, err := do(ctx, env, method, u, headers, nil)
	if err != nil {
		return unreachable(env, err)
	}
	res := classify(resp)
	if res.State == StateOK {
		var m map[string]interface{}
		if json.Unmarshal(resp.body, &m) != nil {
			return Result{State: StateRejected, HTTPStatus: resp.status, Detail: "unexpected non-JSON response: " + upstreamMessage(resp.body)}
		}
		var parts []string
		for _, f := range identityFields {
			if v := lookup(m, f); v != "" {
				parts = append(parts, v)
			}
		}
		res.Identity = strings.Join(parts, " / ")
	}
	return res
}

func checkGitLab(ctx context.Context, env config.Environment, token string) Result {
	base := strings.TrimRight(env.BaseURL, "/")
	res := checkJSON(ctx, env, "GET", base+"/user", bearer(token), "username", "name")
	if res.State != StateOK {
		return res
	}
	resp, err := do(ctx, env, "GET", base+"/personal_access_tokens/self", bearer(token), nil)
	if err == nil && resp.status == 200 {
		var self struct {
			Name      string   `json:"name"`
			ExpiresAt string   `json:"expires_at"`
			Scopes    []string `json:"scopes"`
		}
		if json.Unmarshal(resp.body, &self) == nil {
			res.ExpiresAt = self.ExpiresAt
			res.Scopes = self.Scopes
			if self.Name != "" {
				res.Detail = "token name: " + self.Name
			}
		}
	}
	return res
}

func checkGitHub(ctx context.Context, env config.Environment, token string) Result {
	resp, err := do(ctx, env, "GET", strings.TrimRight(env.BaseURL, "/")+"/user", bearer(token), nil)
	if err != nil {
		return unreachable(env, err)
	}
	res := classify(resp)
	if res.State == StateOK {
		var m map[string]interface{}
		_ = json.Unmarshal(resp.body, &m)
		res.Identity = lookup(m, "login")
		res.ExpiresAt = resp.header.Get("github-authentication-token-expiration")
		if s := resp.header.Get("X-OAuth-Scopes"); s != "" {
			for _, p := range strings.Split(s, ",") {
				res.Scopes = append(res.Scopes, strings.TrimSpace(p))
			}
		}
	}
	return res
}

func checkSlack(ctx context.Context, env config.Environment, xoxc, xoxd string) Result {
	if xoxd == "" {
		return Result{State: StateUnchecked, Detail: "needs both the xoxc token and the d (xoxd) cookie"}
	}
	form := url.Values{"token": {xoxc}}
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Cookie":       "d=" + xoxd,
	}
	resp, err := do(ctx, env, "POST", strings.TrimRight(env.BaseURL, "/")+"/auth.test", headers, strings.NewReader(form.Encode()))
	if err != nil {
		return unreachable(env, err)
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		User  string `json:"user"`
		Team  string `json:"team"`
		URL   string `json:"url"`
	}
	if json.Unmarshal(resp.body, &out) != nil {
		return classify(resp)
	}
	if !out.OK {
		return Result{State: StateRejected, HTTPStatus: resp.status, Detail: out.Error}
	}
	return Result{State: StateOK, HTTPStatus: resp.status, Identity: out.User + " @ " + out.Team, Detail: out.URL}
}

func checkPostgres(env config.Environment, password string) Result {
	host := env.Host
	if host == "" {
		host = env.BaseURL
	}
	port := env.Port
	if port == 0 {
		port = 5432
	}
	names := make([]string, 0, len(env.Databases))
	for n := range env.Databases {
		names = append(names, n)
	}
	sort.Strings(names)
	db := "postgres"
	if len(names) > 0 && env.Databases[names[0]].DB != "" {
		db = env.Databases[names[0]].DB
	}
	c := postgres.NewConnector()
	if err := c.Connect(host, port, env.User, password, db, ""); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "password authentication failed") || strings.Contains(msg, "SASL authentication failed") || strings.Contains(msg, "28P01") || strings.Contains(msg, "28000") {
			return Result{State: StateRejected, Detail: msg}
		}
		return unreachable(env, errors.New(msg))
	}
	c.Close()
	return Result{State: StateOK, Identity: env.User + "@" + db}
}

func checkIMAP(env config.Environment, password string) Result {
	host := env.ImapHost
	if host == "" {
		host = env.Host
	}
	if host == "" {
		host = env.BaseURL
	}
	port := env.ImapPort
	if port == 0 {
		port = 993
	}
	cfg := &email.Config{ImapHost: host, ImapPort: port, ImapTLS: true, User: env.User, Password: password}
	if _, err := email.NewConnector().ListMailboxes(cfg); err != nil {
		msg := err.Error()
		low := strings.ToLower(msg)
		if strings.Contains(low, "auth") || strings.Contains(low, "credentials") || strings.Contains(low, "login") {
			return Result{State: StateRejected, Detail: msg}
		}
		return unreachable(env, err)
	}
	return Result{State: StateOK, Identity: env.User}
}
