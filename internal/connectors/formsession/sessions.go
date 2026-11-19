package formsession

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// loginBackoff is how long a refused login (ErrLoginRefused) is not retried
// for its key (EG0043, owner 2026-11-20: fixed 60 s).
const loginBackoff = 60 * time.Second

// Sessions caches one session cookie per key (workspace, site, environment).
// Each key has its own lock, so concurrent first calls on one key log in once
// while other keys are not held up. A login refused by the site
// (ErrLoginRefused) is not retried for loginBackoff; other failures are not
// cached.
type Sessions struct {
	mu       sync.Mutex
	entries  map[string]*sessionEntry
	waitHook func()
	now      func() time.Time
}

type sessionEntry struct {
	mu     sync.Mutex
	cookie string
	// refused is the last refused login's error, not retried before until.
	refused error
	until   time.Time
}

// NewSessions returns an empty session cache.
func NewSessions() *Sessions {
	return &Sessions{entries: map[string]*sessionEntry{}}
}

// Key builds the cache key for a workspace, site and resolved environment.
func Key(workspace, site, env string) string {
	return workspace + "\x00" + site + "\x00" + env
}

// Cookie returns the cached cookie for key, or calls login (with the key's
// lock held) and caches its result on success. After a login error wrapping
// ErrLoginRefused, calls within loginBackoff return that error with a
// "login not retried for <N>s" suffix and do not call login.
func (s *Sessions) Cookie(key string, login func() (string, error)) (string, error) {
	if s == nil {
		return "", errors.New("form-session sessions not initialised")
	}
	s.mu.Lock()
	e, ok := s.entries[key]
	if !ok {
		e = &sessionEntry{}
		s.entries[key] = e
	}
	hook := s.waitHook
	now := s.now
	s.mu.Unlock()
	if now == nil {
		now = time.Now
	}

	if hook != nil {
		hook()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cookie != "" {
		return e.cookie, nil
	}
	if e.refused != nil {
		if left := e.until.Sub(now()); left > 0 {
			secs := (left + time.Second - 1) / time.Second
			return "", fmt.Errorf("%w; login not retried for %ds", e.refused, secs)
		}
	}
	cookie, err := login()
	if err != nil {
		if errors.Is(err, ErrLoginRefused) {
			e.refused, e.until = err, now().Add(loginBackoff)
		} else {
			e.refused = nil
		}
		return "", err
	}
	e.refused = nil
	e.cookie = cookie
	return cookie, nil
}

// Invalidate drops key's cookie only if it is still stale, so a newer cookie
// stored by a concurrent re-login survives.
func (s *Sessions) Invalidate(key, stale string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	e, ok := s.entries[key]
	s.mu.Unlock()
	if !ok {
		return
	}
	e.mu.Lock()
	if e.cookie == stale {
		e.cookie = ""
	}
	e.mu.Unlock()
}

// Clear drops every session (config reload, secret write). A login in flight
// stores into its orphaned entry and is lost; the next call logs in again.
func (s *Sessions) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.entries = map[string]*sessionEntry{}
	s.mu.Unlock()
}

// SetWaitHook is a test hook: f runs in Cookie after the entry is found and
// before its lock is taken. Test-only (the server package's tests use it, so
// it cannot live in an _test file); nil in production.
func (s *Sessions) SetWaitHook(f func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.waitHook = f
	s.mu.Unlock()
}

// SetNow is a test hook: f replaces time.Now for the login backoff.
// Test-only, like SetWaitHook; nil restores time.Now.
func (s *Sessions) SetNow(f func() time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.now = f
	s.mu.Unlock()
}
