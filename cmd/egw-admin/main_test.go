package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
	"github.com/external_gateway/internal/server"
	"github.com/rs/zerolog"
)

type recStore struct {
	mu      sync.Mutex
	deletes []string
}

func (r *recStore) Resolve(ws, n string) ([]byte, error) { return nil, nil }
func (r *recStore) Set(ws, n string, v []byte) error     { return nil }
func (r *recStore) Delete(ws, n string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletes = append(r.deletes, ws+"/"+n)
	return nil
}

// The delete request the client builds must be understood by the real handler.
func TestDeleteRequestAcceptedByServer(t *testing.T) {
	rs := &recStore{}
	secrets.SetStore(rs)
	h := sha256.Sum256([]byte("tok"))
	cfg := &config.Config{AdminTokenSHA: hex.EncodeToString(h[:])}
	srv := httptest.NewServer(server.New(cfg, &policy.OperationRegistry{Operations: map[string]*policy.Operation{}}, "", zerolog.Nop()))
	defer srv.Close()

	*token = "tok"
	req, err := newAdminRequest(http.MethodDelete, deleteURL(srv.URL+"/admin", "acme", "pg-prod"), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(rs.deletes) != 1 || rs.deletes[0] != "acme/pg-prod" {
		t.Fatalf("status %d deletes %v", resp.StatusCode, rs.deletes)
	}
}

func TestDeleteURLEscapes(t *testing.T) {
	got := deleteURL("http://h/admin", "a&b", "n m")
	want := "http://h/admin/secrets?name=n+m&workspace=a%26b"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
