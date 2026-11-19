package formsession

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoginWrapsRefusal(t *testing.T) {
	setCookie := func(w http.ResponseWriter, v string) {
		http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: v})
	}
	redirect := func(w http.ResponseWriter, to string) {
		w.Header().Set("Location", to)
		w.WriteHeader(http.StatusFound)
	}
	for _, tc := range []struct {
		name    string
		answer  func(w http.ResponseWriter)
		refused bool
	}{
		{"401", func(w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) }, true},
		{"403", func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) }, true},
		{"302 to login", func(w http.ResponseWriter) { setCookie(w, "c"); redirect(w, "/login") }, true},
		{"200 no cookie", func(w http.ResponseWriter) { w.Write([]byte("login page")) }, true},
		{"200 empty cookie", func(w http.ResponseWriter) { setCookie(w, ""); w.Write([]byte("x")) }, true},
		{"302 elsewhere no cookie", func(w http.ResponseWriter) { redirect(w, "/") }, true},
		{"404", func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) }, true},
		{"429", func(w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) }, true},
		{"500", func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }, false},
		{"503 with cookie", func(w http.ResponseWriter) { setCookie(w, "c"); w.WriteHeader(http.StatusServiceUnavailable) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tc.answer(w) }))
			defer srv.Close()
			_, err := NewConnector().Login(context.Background(), srv.URL, "u", "p", false)
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errors.Is(err, ErrLoginRefused); got != tc.refused {
				t.Fatalf("errors.Is(%q, ErrLoginRefused) = %v, want %v", err, got, tc.refused)
			}
		})
	}
	t.Run("transport", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		_, err := NewConnector().Login(context.Background(), url, "u", "p", false)
		if err == nil || errors.Is(err, ErrLoginRefused) {
			t.Fatalf("want a transport error not wrapping ErrLoginRefused, got %v", err)
		}
	})
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			setCookie(w, "sid")
			redirect(w, "/")
		}))
		defer srv.Close()
		if c, err := NewConnector().Login(context.Background(), srv.URL, "u", "p", false); err != nil || c != "sid" {
			t.Fatalf("got %q, %v", c, err)
		}
	})
}
