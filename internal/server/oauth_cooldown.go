package server

import (
	"sync"
	"time"

	"github.com/external_gateway/internal/config"
)

// defaultOAuthRefreshCooldown is the window when an env's oauth block sets no
// refresh_cooldown (EG0044).
const defaultOAuthRefreshCooldown = 5 * time.Minute

// cooldownWindow is env's refresh cooldown: the configured one, else the default.
func cooldownWindow(env *config.Environment) time.Duration {
	if env.OAuth != nil && env.OAuth.RefreshCooldown != nil {
		return *env.OAuth.RefreshCooldown
	}
	return defaultOAuthRefreshCooldown
}

// cooldownKey identifies the access token a refresh renews; stable across reloads.
func cooldownKey(workspace string, env *config.Environment) string {
	return workspace + "\x00" + env.SecretRef
}

// oauthCooldown remembers, per access token, until when a rejected call skips
// the token endpoint (EG0044): a refresh failed, or its retry was still rejected,
// so a dead refresh token is not presented on every call.
type oauthCooldown struct {
	mu    sync.Mutex
	until map[string]time.Time
	gen   uint64 // bumped by clear; a start with an older generation is dropped
	now   func() time.Time
}

func newOAuthCooldown() *oauthCooldown {
	return &oauthCooldown{until: map[string]time.Time{}, now: time.Now}
}

// generation is the value to hand back to start. Nil-safe, like every method:
// test servers built without New have no cooldown and never cool.
func (c *oauthCooldown) generation() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// cooling reports whether key is inside its window; an expired entry is dropped.
func (c *oauthCooldown) cooling(key string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.until[key]
	if !ok {
		return false
	}
	if c.now().Before(until) {
		return true
	}
	delete(c.until, key)
	return false
}

// start opens key's window unless a clear happened since gen was read or the
// window is off (<= 0).
func (c *oauthCooldown) start(key string, gen uint64, window time.Duration) {
	if c == nil || window <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	c.until[key] = c.now().Add(window)
}

// clear drops every window (a secret was set or deleted, or the config reloaded).
func (c *oauthCooldown) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.until = map[string]time.Time{}
	c.gen++
	c.mu.Unlock()
}
