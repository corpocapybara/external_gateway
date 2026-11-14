package formsession

import (
	"errors"
	"sync"
)

// Sessions caches one session cookie per key (workspace, site, environment).
// Each key has its own lock, so concurrent first calls on one key log in once
// while other keys are not held up. A failed login is not cached.
type Sessions struct {
	mu       sync.Mutex
	entries  map[string]*sessionEntry
	waitHook func()
}

type sessionEntry struct {
	mu     sync.Mutex
	cookie string
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
// lock held) and caches its result on success.
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
	s.mu.Unlock()

	if hook != nil {
		hook()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cookie != "" {
		return e.cookie, nil
	}
	cookie, err := login()
	if err != nil {
		return "", err
	}
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
