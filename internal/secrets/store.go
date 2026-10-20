package secrets

import "time"

type SecretStore interface {
	Resolve(workspace, name string) ([]byte, error)
	Set(workspace, name string, value []byte) error
	Delete(workspace, name string) error
}

// InfoStore is implemented by stores that can report when a secret was last written.
type InfoStore interface {
	LastWritten(workspace, name string) (time.Time, bool)
}
