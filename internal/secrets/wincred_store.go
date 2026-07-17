package secrets

import (
	"fmt"

	"github.com/danieljoos/wincred"
)

type WinCredStore struct{}

func (s *WinCredStore) Resolve(workspace, name string) ([]byte, error) {
	target := fmt.Sprintf("egw/%s/%s", workspace, name)
	creds, err := wincred.GetGenericCredential(target)
	if err != nil {
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
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		return err
	}
	return cred.Delete()
}
