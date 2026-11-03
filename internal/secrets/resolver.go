package secrets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type Resolver struct {
	mu    sync.RWMutex
	cache map[string]*cachedSecret
	ttl   time.Duration
	store SecretStore
}

type cachedSecret struct {
	value    []byte
	resolved time.Time
}

type TaintRegistry struct {
	mu     sync.RWMutex
	seen   map[string]bool // sha256(value) -> present, for dedup
	values [][]byte        // known secret values, redacted from outbound data
}

var redactPlaceholder = []byte("***")

var (
	globalResolver *Resolver
	globalTaint    *TaintRegistry
	resolverOnce   sync.Once
	taintOnce      sync.Once
)

func SetStore(store SecretStore) {
	r := GetResolver()
	r.mu.Lock()
	r.store = store
	r.mu.Unlock()
}

func GetResolver() *Resolver {
	resolverOnce.Do(func() {
		globalResolver = &Resolver{
			cache: make(map[string]*cachedSecret),
			ttl:   30 * time.Second,
			store: &WinCredStore{},
		}
	})
	return globalResolver
}

func GetTaintRegistry() *TaintRegistry {
	taintOnce.Do(func() {
		globalTaint = &TaintRegistry{
			seen: make(map[string]bool),
		}
	})
	return globalTaint
}

func (r *Resolver) Resolve(workspace, name string) ([]byte, error) {
	cacheKey := fmt.Sprintf("%s/%s", workspace, name)

	r.mu.RLock()
	if cached, ok := r.cache[cacheKey]; ok {
		if time.Since(cached.resolved) < r.ttl {
			r.mu.RUnlock()
			return checkEmpty(cached.value)
		}
	}
	r.mu.RUnlock()

	secret, err := r.store.Resolve(workspace, name)
	if err != nil {
		return nil, err
	}
	if _, err := checkEmpty(secret); err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[cacheKey] = &cachedSecret{
		value:    secret,
		resolved: time.Now(),
	}
	r.mu.Unlock()

	GetTaintRegistry().Taint(secret)

	return secret, nil
}

func checkEmpty(secret []byte) ([]byte, error) {
	if len(secret) <= 1 {
		return nil, fmt.Errorf("secret is empty or not set (len=%d)", len(secret))
	}
	return secret, nil
}

func (r *Resolver) Set(workspace, name string, value []byte) error {
	defer r.forget(workspace, name)
	return r.store.Set(workspace, name, value)
}

func (r *Resolver) Delete(workspace, name string) error {
	defer r.forget(workspace, name)
	return r.store.Delete(workspace, name)
}

func (r *Resolver) forget(workspace, name string) {
	r.mu.Lock()
	delete(r.cache, fmt.Sprintf("%s/%s", workspace, name))
	r.mu.Unlock()
}

// LastWritten reports when the secret was last written, if the store tracks it.
func (r *Resolver) LastWritten(workspace, name string) (time.Time, bool) {
	if s, ok := r.store.(InfoStore); ok {
		return s.LastWritten(workspace, name)
	}
	return time.Time{}, false
}

func (r *Resolver) FlushCache() {
	r.mu.Lock()
	r.cache = make(map[string]*cachedSecret)
	r.mu.Unlock()
}

// Taint registers a secret for outbound redaction, together with the forms it
// takes once the gateway handles it: the whitespace-trimmed value (headers use
// strings.TrimSpace) and the JSON-escaped forms (with and without HTML
// escaping), since responses are redacted after json.Marshal.
//
// Values shorter than 5 bytes are ignored on purpose: redacting a 1-4 byte
// string would mangle unrelated response content, and such a value is not a
// credential worth protecting (a brute-force guess space this small is no
// secret). The check applies to each variant separately.
func (t *TaintRegistry) Taint(secret []byte) {
	t.taintExact(secret)
	trimmed := bytes.TrimSpace(secret)
	t.taintExact(trimmed)
	for _, v := range [][]byte{secret, trimmed} {
		t.taintExact(jsonEscaped(v, true))
		t.taintExact(jsonEscaped(v, false))
	}
}

// jsonEscaped returns v as it appears inside a JSON string literal (quotes
// stripped), with HTML escaping of < > & on or off.
func jsonEscaped(v []byte, escapeHTML bool) []byte {
	if len(v) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(string(v)); err != nil {
		return nil
	}
	b := bytes.TrimRight(buf.Bytes(), "\n")
	if len(b) < 2 {
		return nil
	}
	return append([]byte(nil), b[1:len(b)-1]...)
}

func (t *TaintRegistry) taintExact(secret []byte) {
	if len(secret) < 5 {
		return
	}
	hash := sha256.Sum256(secret)
	key := hex.EncodeToString(hash[:])
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen[key] {
		return
	}
	t.seen[key] = true
	cp := make([]byte, len(secret))
	copy(cp, secret)
	t.values = append(t.values, cp)
}

func (t *TaintRegistry) IsTainted(secret []byte) bool {
	hash := sha256.Sum256(secret)
	key := hex.EncodeToString(hash[:])
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.seen[key]
}

// Redact replaces every occurrence of a known secret value in data with "***".
// This is the outbound backstop: even if response shaping is disabled for an
// operation, or an upstream echoes a credential back, the raw secret value does
// not reach the caller. Values are matched as exact byte substrings.
func (t *TaintRegistry) Redact(data []byte) []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.values) == 0 {
		return data
	}
	out := data
	for _, v := range t.values {
		if len(v) == 0 {
			continue
		}
		out = bytes.ReplaceAll(out, v, redactPlaceholder)
	}
	return out
}

// RedactString is the string convenience wrapper around Redact.
func (t *TaintRegistry) RedactString(s string) string {
	return string(t.Redact([]byte(s)))
}

func ResolveWithWorkspaceCheck(workspace, name, requestWorkspace string) ([]byte, error) {
	if workspace != requestWorkspace {
		return nil, fmt.Errorf("cross-tenant secret access denied: %s/%s from workspace %s", workspace, name, requestWorkspace)
	}
	return GetResolver().Resolve(workspace, name)
}

func ResolveSecretRef(ref string) (workspace, name string, err error) {
	if len(ref) < 9 || ref[:9] != "secret://" {
		err = fmt.Errorf("invalid secret ref format: %s", ref)
		return
	}
	rest := ref[9:]
	slashIdx := -1
	for i, c := range rest {
		if c == '/' {
			slashIdx = i
			break
		}
	}
	if slashIdx == -1 {
		workspace = rest
		name = ""
		return
	}
	workspace = rest[:slashIdx]
	name = rest[slashIdx+1:]
	return
}
