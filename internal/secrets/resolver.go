package secrets

import (
	"crypto/sha256"
	"encoding/hex"
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
	mu    sync.RWMutex
	taint map[string]bool
}

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
			taint: make(map[string]bool),
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
	return r.store.Set(workspace, name, value)
}

func (r *Resolver) Delete(workspace, name string) error {
	return r.store.Delete(workspace, name)
}

func (r *Resolver) FlushCache() {
	r.mu.Lock()
	r.cache = make(map[string]*cachedSecret)
	r.mu.Unlock()
}

func (t *TaintRegistry) Taint(secret []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	hash := sha256.Sum256(secret)
	t.taint[hex.EncodeToString(hash[:])] = true
}

func (t *TaintRegistry) IsTainted(secret []byte) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	hash := sha256.Sum256(secret)
	return t.taint[hex.EncodeToString(hash[:])]
}

func (t *TaintRegistry) Redact(data []byte) []byte {
	result := make([]byte, len(data))
	copy(result, data)
	for i := range result {
		if result[i] != 0 {
			result[i] = '*'
		}
	}
	return result
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
