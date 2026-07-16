package connectors

import (
	"context"
)

type Request struct {
	Method      string
	Path        string
	Headers     map[string]string
	Body        []byte
	Credentials *Credentials
	Insecure    bool // skip TLS verification for this request (internal self-signed hosts)
}

type Credentials struct {
	Username string
	Password string
	Token    string
	APIKey   string
}

type Response struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
	Error      error
}

type Connector interface {
	Name() string
	Execute(ctx context.Context, req *Request) (*Response, error)
}

type ConnectorRegistry struct {
	connectors map[string]Connector
}

func NewConnectorRegistry() *ConnectorRegistry {
	return &ConnectorRegistry{
		connectors: make(map[string]Connector),
	}
}

func (r *ConnectorRegistry) Register(c Connector) {
	r.connectors[c.Name()] = c
}

func (r *ConnectorRegistry) Get(name string) (Connector, bool) {
	c, ok := r.connectors[name]
	return c, ok
}
