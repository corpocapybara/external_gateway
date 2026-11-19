package formsession

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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

// fakeClock is an injectable clock for the login backoff.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClockedSessions() (*Sessions, *fakeClock) {
	s := NewSessions()
	c := &fakeClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	s.SetNow(c.now)
	return s, c
}

var errRefused = fmt.Errorf("%w (status 401)", ErrLoginRefused)

// counting returns a login func that answers with err (or cookie on nil) and
// counts its calls.
func counting(n *int, cookie string, err error) func() (string, error) {
	return func() (string, error) {
		*n++
		if err != nil {
			return "", err
		}
		return cookie, nil
	}
}

func TestFormSessionBackoffAfterRefusal(t *testing.T) {
	s, clock := newClockedSessions()
	k := Key("ws", "codimd", "prod")
	logins := 0
	if _, err := s.Cookie(k, counting(&logins, "", errRefused)); err != errRefused {
		t.Fatalf("first call: %v", err)
	}
	_, err := s.Cookie(k, counting(&logins, "c", nil))
	if want := "form-session login refused (status 401); login not retried for 60s"; err == nil || err.Error() != want {
		t.Fatalf("in backoff: %v, want %q", err, want)
	}
	if !errors.Is(err, ErrLoginRefused) {
		t.Fatal("backoff error must wrap ErrLoginRefused")
	}
	clock.add(30 * time.Second)
	_, err = s.Cookie(k, counting(&logins, "c", nil))
	if want := "form-session login refused (status 401); login not retried for 30s"; err == nil || err.Error() != want {
		t.Fatalf("30 s later: %v, want %q", err, want)
	}
	if logins != 1 {
		t.Fatalf("logins %d, want 1", logins)
	}
}

func TestFormSessionNoBackoffOnOtherError(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	loginTo := func(url string) error {
		_, err := NewConnector().Login(context.Background(), url, "u", "p", false)
		return err
	}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"transport", loginTo(closedURL)},
		{"5xx", loginTo(failing.URL)},
		{"secret resolve", errors.New("secret resolve failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || errors.Is(tc.err, ErrLoginRefused) {
				t.Fatalf("bad fixture error %v", tc.err)
			}
			s, _ := newClockedSessions()
			k := Key("ws", "codimd", "prod")
			logins := 0
			if _, err := s.Cookie(k, counting(&logins, "", tc.err)); err != tc.err {
				t.Fatalf("first call: %v", err)
			}
			if c, err := s.Cookie(k, counting(&logins, "c", nil)); err != nil || c != "c" || logins != 2 {
				t.Fatalf("second call %q, %v after %d logins, want c after 2", c, err, logins)
			}
		})
	}
}

func TestFormSessionBackoffExpires(t *testing.T) {
	k := Key("ws", "codimd", "prod")
	t.Run("boundary", func(t *testing.T) {
		s, clock := newClockedSessions()
		logins := 0
		s.Cookie(k, counting(&logins, "", errRefused))
		clock.add(59500 * time.Millisecond)
		_, err := s.Cookie(k, counting(&logins, "c", nil))
		if want := "form-session login refused (status 401); login not retried for 1s"; err == nil || err.Error() != want {
			t.Fatalf("at +59.5 s: %v, want %q", err, want)
		}
		clock.add(500 * time.Millisecond)
		if c, err := s.Cookie(k, counting(&logins, "c", nil)); err != nil || c != "c" || logins != 2 {
			t.Fatalf("at +60 s: %q, %v, %d logins", c, err, logins)
		}
		if s.entries[k].refused != nil {
			t.Fatal("a success must clear the backoff")
		}
	})
	t.Run("window starts when the login returns", func(t *testing.T) {
		s, clock := newClockedSessions()
		logins := 0
		slow := func() (string, error) { logins++; clock.add(10 * time.Second); return "", errRefused }
		s.Cookie(k, slow)
		clock.add(59 * time.Second)
		if _, err := s.Cookie(k, counting(&logins, "c", nil)); err == nil || logins != 1 {
			t.Fatalf("59 s after the login returned: %v, %d logins", err, logins)
		}
	})
	t.Run("other error after expiry clears", func(t *testing.T) {
		s, clock := newClockedSessions()
		logins := 0
		s.Cookie(k, counting(&logins, "", errRefused))
		clock.add(61 * time.Second)
		other := errors.New("secret resolve failed")
		if _, err := s.Cookie(k, counting(&logins, "", other)); err != other || logins != 2 {
			t.Fatalf("after expiry: %v, %d logins", err, logins)
		}
		if s.entries[k].refused != nil {
			t.Fatal("a non-refusal error must clear the backoff")
		}
	})
}

func TestFormSessionBackoffPerKey(t *testing.T) {
	s, _ := newClockedSessions()
	a, b := Key("ws", "codimd", "prod"), Key("ws", "codimd", "test")
	logins := 0
	s.Cookie(a, counting(&logins, "", errRefused))
	if c, err := s.Cookie(b, counting(&logins, "cb", nil)); err != nil || c != "cb" {
		t.Fatalf("other key: %q, %v", c, err)
	}
	if _, err := s.Cookie(a, counting(&logins, "ca", nil)); err == nil || logins != 2 {
		t.Fatalf("refused key: %v after %d logins, want backoff after 2", err, logins)
	}
}

func TestFormSessionBackoffConcurrent(t *testing.T) {
	const callers = 3
	s, _ := newClockedSessions()
	k := Key("ws", "codimd", "prod")
	arrived := make(chan struct{}, 16)
	s.SetWaitHook(func() { arrived <- struct{}{} })
	var mu sync.Mutex
	logins := 0
	login := func() (string, error) {
		mu.Lock()
		logins++
		mu.Unlock()
		deadline := time.After(5 * time.Second)
		for i := 0; i < callers; i++ {
			select {
			case <-arrived:
			case <-deadline:
				t.Errorf("only %d of %d callers arrived", i, callers)
				return "", errRefused
			}
		}
		return "", errRefused
	}
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Cookie(k, login)
		}(i)
	}
	wg.Wait()
	if logins != 1 {
		t.Fatalf("logins %d, want 1", logins)
	}
	for i, err := range errs {
		if !errors.Is(err, ErrLoginRefused) {
			t.Errorf("caller %d: %v", i, err)
		}
	}
}

func TestFormSessionClearDuringRefusedLogin(t *testing.T) {
	s, _ := newClockedSessions()
	k := Key("ws", "codimd", "prod")
	entered, release := make(chan struct{}), make(chan struct{})
	logins := 0
	done := make(chan error)
	go func() {
		_, err := s.Cookie(k, func() (string, error) {
			logins++
			close(entered)
			<-release
			return "", errRefused
		})
		done <- err
	}()
	<-entered
	s.Clear()
	close(release)
	if err := <-done; err != errRefused {
		t.Fatalf("blocked caller: %v, want the refusal", err)
	}
	if c, err := s.Cookie(k, counting(&logins, "c", nil)); err != nil || c != "c" || logins != 2 {
		t.Fatalf("after Clear: %q, %v, %d logins", c, err, logins)
	}
}
