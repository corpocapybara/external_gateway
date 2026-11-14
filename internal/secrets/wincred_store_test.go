package secrets

import (
	"errors"
	"testing"
	"time"

	"github.com/danieljoos/wincred"
)

func stubGetGenericCredential(t *testing.T, cred *wincred.GenericCredential, err error) {
	t.Helper()
	orig := getGenericCredential
	getGenericCredential = func(string) (*wincred.GenericCredential, error) { return cred, err }
	t.Cleanup(func() { getGenericCredential = orig })
}

func TestWinCredStoreNilCredential(t *testing.T) {
	const want = "credential not found: egw/ws/name"
	s := &WinCredStore{}

	t.Run("Resolve", func(t *testing.T) {
		stubGetGenericCredential(t, nil, nil)
		secret, err := s.Resolve("ws", "name")
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
		if secret != nil {
			t.Fatalf("secret = %v, want nil", secret)
		}
	})

	t.Run("LastWritten", func(t *testing.T) {
		stubGetGenericCredential(t, nil, nil)
		at, ok := s.LastWritten("ws", "name")
		if ok || !at.Equal(time.Time{}) {
			t.Fatalf("LastWritten = (%v, %v), want (zero, false)", at, ok)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		stubGetGenericCredential(t, nil, nil)
		err := s.Delete("ws", "name")
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("ResolveLookupError", func(t *testing.T) {
		stubGetGenericCredential(t, nil, errors.New("Element not found."))
		secret, err := s.Resolve("ws", "name")
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
		if secret != nil {
			t.Fatalf("secret = %v, want nil", secret)
		}
	})
}
