package secrets

import (
	"fmt"
	"time"

	"github.com/danieljoos/wincred"
)

// getGenericCredential is the credential lookup used by WinCredStore; tests
// replace it to simulate lookup results that cannot be produced on demand.
var getGenericCredential = wincred.GetGenericCredential

type WinCredStore struct{}

func (s *WinCredStore) Resolve(workspace, name string) ([]byte, error) {
	target := fmt.Sprintf("egw/%s/%s", workspace, name)
	creds, err := getGenericCredential(target)
	if err != nil || creds == nil {
		return nil, fmt.Errorf("credential not found: %s", target)
	}
	secret := make([]byte, len(creds.CredentialBlob))
	copy(secret, creds.CredentialBlob)
	if _, err := checkEmpty(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

func (s *WinCredStore) Set(workspace, name string, value []byte) error {
	target := fmt.Sprintf("egw/%s/%s", workspace, name)
	cred := wincred.NewGenericCredential(target)
	cred.CredentialBlob = value
	return cred.Write()
}

func (s *WinCredStore) Delete(workspace, name string) error {
	target := fmt.Sprintf("egw/%s/%s", workspace, name)
	cred, err := getGenericCredential(target)
	if err != nil {
		return err
	}
	if cred == nil {
		return fmt.Errorf("credential not found: %s", target)
	}
	return cred.Delete()
}

func (s *WinCredStore) LastWritten(workspace, name string) (time.Time, bool) {
	cred, err := getGenericCredential(fmt.Sprintf("egw/%s/%s", workspace, name))
	if err != nil || cred == nil || len(cred.CredentialBlob) <= 1 {
		return time.Time{}, false
	}
	return cred.LastWritten, true
}
