package secrets

type SecretStore interface {
	Resolve(workspace, name string) ([]byte, error)
	Set(workspace, name string, value []byte) error
	Delete(workspace, name string) error
}
