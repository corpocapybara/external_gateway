package server

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
	"github.com/external_gateway/internal/tokencheck"
)

// credentialEntry is what /capabilities tells an agent about one token it depends on.
// Built from the check's state, HTTP status and expiry only: never the secret,
// identity, token name or scopes.
type credentialEntry struct {
	Site      string `json:"site"`
	Env       string `json:"env"`
	State     string `json:"state"`
	ExpiresAt string `json:"expires_at,omitempty"`
	DaysLeft  *int   `json:"days_left,omitempty"`
	Warning   string `json:"warning,omitempty"`
}

const (
	credWarnDays    = 7
	credTTLOK       = time.Hour
	credTTLOther    = 5 * time.Minute
	credDefaultWait = 5 * time.Second
)

// credCached is one check result, kept so a warm /capabilities makes no upstream call.
type credCached struct {
	res        tokencheck.Result
	resolveErr error
	at         time.Time
}

// credCache holds token-check results per workspace/site/env.
type credCache struct {
	mu      sync.Mutex
	entries map[string]credCached
	gen     uint64 // bumped by clear; a check started before a clear does not store its result
	now     func() time.Time
	timeout time.Duration
}

func newCredCache() *credCache {
	return &credCache{entries: map[string]credCached{}, now: time.Now, timeout: credDefaultWait}
}

// clear drops every result (a secret was set or deleted, or the config reloaded).
// Nil-safe for test servers built without New.
func (c *credCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = map[string]credCached{}
	c.gen++
	c.mu.Unlock()
}

// get returns the cached result for key, whether it is still fresh, and the cache
// generation to hand back to put.
func (c *credCache) get(key string) (credCached, bool, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return e, false, c.gen
	}
	ttl := credTTLOther
	if e.resolveErr == nil && e.res.State == tokencheck.StateOK {
		ttl = credTTLOK
	}
	return e, c.now().Sub(e.at) < ttl, c.gen
}

// put stores e unless the cache was cleared since gen was read.
func (c *credCache) put(key string, gen uint64, e credCached) {
	c.mu.Lock()
	if gen == c.gen {
		c.entries[key] = e
	}
	c.mu.Unlock()
}

// credentialStatus reports the GitLab/GitHub tokens behind the agent's granted ops in
// the workspace: one entry per site/env with a secret_ref, sorted by site then env.
func (s *Server) credentialStatus(workspace string, ws *config.Workspace, ops map[string]*policy.Operation) []credentialEntry {
	type target struct {
		site, env, key string
		cfg            config.Environment
	}
	seen := map[string]bool{}
	var targets []target
	for _, op := range ops {
		if seen[op.Site] {
			continue
		}
		seen[op.Site] = true
		site, ok := ws.Sites[op.Site]
		if !ok || site.Disabled {
			continue
		}
		for envName, env := range site.Environments {
			if env.SecretRef == "" {
				continue
			}
			switch tokencheck.DetectKind(op.Site, env) {
			case tokencheck.KindGitLab, tokencheck.KindGitHub:
				targets = append(targets, target{op.Site, envName, workspace + "/" + op.Site + "/" + envName, env})
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].site != targets[j].site {
			return targets[i].site < targets[j].site
		}
		return targets[i].env < targets[j].env
	})

	results := make([]credCached, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		e, fresh, gen := s.creds.get(t.key)
		if fresh {
			results[i] = e
			continue
		}
		wg.Add(1)
		go func(i int, t target, gen uint64) {
			defer wg.Done()
			e := s.checkCredential(workspace, t.site, t.cfg)
			s.creds.put(t.key, gen, e)
			results[i] = e
		}(i, t, gen)
	}
	wg.Wait()

	now := s.creds.now()
	out := make([]credentialEntry, 0, len(targets))
	for i, t := range targets {
		out = append(out, entryFor(t.site, t.env, results[i].res, results[i].resolveErr, now))
	}
	return out
}

func (s *Server) checkCredential(workspace, site string, env config.Environment) credCached {
	at := s.creds.now()
	refWS, name := parseSecretRef(env.SecretRef)
	secret, err := secrets.ResolveWithWorkspaceCheck(refWS, name, workspace)
	if err != nil {
		return credCached{resolveErr: err, at: at}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.creds.timeout)
	defer cancel()
	return credCached{res: tokencheck.Check(ctx, tokencheck.DetectKind(site, env), env, string(secret), ""), at: at}
}

// entryFor turns one check into what the agent sees; days_left is computed from now on
// every call, so a cached result never goes stale across midnight.
func entryFor(site, env string, res tokencheck.Result, resolveErr error, now time.Time) credentialEntry {
	e := credentialEntry{Site: site, Env: env}
	where := site + "/" + env
	switch {
	case resolveErr != nil || res.State == tokencheck.StateUnchecked:
		e.State = "missing"
		e.Warning = "gateway has no usable stored token for " + where + " (not set, empty or unreadable): ask the owner to check it"
	case res.State == tokencheck.StateRejected && (res.HTTPStatus == 401 || res.HTTPStatus == 403):
		e.State = "rejected"
		e.Warning = fmt.Sprintf("%s token rejected by upstream (HTTP %d): ask the owner to renew it; calls on this site fail", where, res.HTTPStatus)
	case res.State == tokencheck.StateOK:
		e.State = "ok"
		e.ExpiresAt = res.ExpiresAt
		if days, ok := daysUntilDate(res.ExpiresAt, now); ok {
			e.DaysLeft = &days
			date := res.ExpiresAt[:10]
			switch {
			case days < 0:
				e.Warning = fmt.Sprintf("%s token expired on %s: ask the owner to renew it; calls on this site fail", where, date)
			case days == 0:
				e.Warning = fmt.Sprintf("%s token expires today (%s): ask the owner to renew it; calls will fail after that", where, date)
			case days <= credWarnDays:
				e.Warning = fmt.Sprintf("%s token expires in %d days (%s): ask the owner to renew it; calls will fail after that", where, days, date)
			}
		}
	default:
		e.State = "unreachable"
	}
	return e
}

// daysUntilDate counts calendar days from now's local date to the date in the first
// ten characters of s (GitLab "2026-11-15", GitHub "2026-11-15 12:00:00 UTC").
func daysUntilDate(s string, now time.Time) (int, bool) {
	if len(s) < 10 {
		return 0, false
	}
	exp, err := time.ParseInLocation("2006-01-02", s[:10], time.Local)
	if err != nil {
		return 0, false
	}
	n := now.In(time.Local)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
	// Round: a DST change makes a calendar day 23 or 25 hours.
	return int(math.Round(exp.Sub(today).Hours() / 24)), true
}
