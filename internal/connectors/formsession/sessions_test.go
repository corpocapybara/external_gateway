package formsession

import (
	"errors"
	"testing"
)

func TestFormSessionInvalidateKeepsNewerCookie(t *testing.T) {
	s := NewSessions()
	k := Key("ws", "codimd", "prod")
	logins := 0
	login := func(c string) func() (string, error) {
		return func() (string, error) { logins++; return c, nil }
	}
	if c, _ := s.Cookie(k, login("old")); c != "old" {
		t.Fatalf("first cookie %q", c)
	}
	// A concurrent caller already re-logged in: the cache holds "new".
	s.Invalidate(k, "old")
	if c, _ := s.Cookie(k, login("new")); c != "new" {
		t.Fatalf("after re-login %q", c)
	}
	// A late Invalidate of the stale cookie must keep the newer one.
	s.Invalidate(k, "old")
	if c, _ := s.Cookie(k, login("third")); c != "new" || logins != 2 {
		t.Fatalf("cookie %q after %d logins, want new after 2", c, logins)
	}
	// A failed login is not cached.
	s.Clear()
	if _, err := s.Cookie(k, func() (string, error) { return "", errors.New("refused") }); err == nil {
		t.Fatal("want the login error")
	}
	if c, _ := s.Cookie(k, login("fresh")); c != "fresh" {
		t.Fatalf("after a failed login %q", c)
	}
	// Nil receiver: Cookie errors, Clear and Invalidate do nothing.
	var none *Sessions
	none.Clear()
	none.Invalidate(k, "x")
	if _, err := none.Cookie(k, login("x")); err == nil {
		t.Fatal("nil Sessions must refuse")
	}
}

func TestFormSessionNeedsLogin(t *testing.T) {
	for _, tc := range []struct {
		status   int
		location string
		want     bool
	}{
		{401, "", true},
		{302, "/login", true},
		{302, "http://h/login", true},
		{303, "/x/login", true},
		{302, "/login?next=/n", true},
		{302, "/", false},
		{302, "/loginx", false},
		{302, "", false},
		{403, "", false},
		{200, "/login", false},
	} {
		if got := NeedsLogin(tc.status, tc.location); got != tc.want {
			t.Errorf("NeedsLogin(%d, %q) = %v, want %v", tc.status, tc.location, got, tc.want)
		}
	}
}
