package httprest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/external_gateway/internal/connectors"
)

func TestDefaultBodyCapApplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for i := int64(0); i <= DefaultMaxBodyBytes>>20; i++ {
			w.Write(chunk)
		}
	}))
	defer srv.Close()
	_, err := NewConnector().Execute(context.Background(), &connectors.Request{Method: "GET", Path: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("expected cap error, got %v", err)
	}
}

func TestSmallBodyUnderDefaultCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	resp, err := NewConnector().Execute(context.Background(), &connectors.Request{Method: "GET", Path: srv.URL})
	if err != nil || string(resp.Body) != "ok" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}
