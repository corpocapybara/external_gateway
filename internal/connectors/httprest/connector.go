package httprest

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/external_gateway/internal/connectors"
	"github.com/external_gateway/internal/secrets"
)

type Connector struct {
	client   *http.Client
	insecure *http.Client // skips TLS verification (internal self-signed hosts only)
}

// noRedirect stops the client from following redirects. Following them silently
// turns a POST/PUT write into a bodiless GET on 301/302/303 (the write is lost
// but the redirect's 2xx is reported as success), and a cross-host redirect
// would resend secret headers to the new host. Returning ErrUseLastResponse
// surfaces the redirect response to the caller instead.
func noRedirect(req *http.Request, via []*http.Request) error {
	return http.ErrUseLastResponse
}

func NewConnector() *Connector {
	return &Connector{
		client: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: noRedirect,
		},
		insecure: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: noRedirect,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

func (c *Connector) Name() string {
	return "http-rest"
}

func (c *Connector) Execute(ctx context.Context, req *connectors.Request) (*connectors.Response, error) {
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.Path, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	if req.Credentials != nil {
		if req.Credentials.Username != "" && req.Credentials.Password != "" {
			httpReq.SetBasicAuth(req.Credentials.Username, req.Credentials.Password)
		} else if req.Credentials.Token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+req.Credentials.Token)
		} else if req.Credentials.APIKey != "" {
			for k := range req.Headers {
				if strings.Contains(strings.ToLower(k), "apikey") {
					httpReq.Header.Set(k, req.Credentials.APIKey)
				}
			}
		}
	}

	client := c.client
	if req.Insecure {
		client = c.insecure
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		// The wrapped *url.Error carries the full request URL, which may include
		// secrets in query parameters (e.g. Trello key/token). Unwrap it so the
		// error returned to (and surfaced to) the agent never echoes the URL.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, fmt.Errorf("executing %s request: %w", req.Method, urlErr.Err)
		}
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	var bodyReader io.Reader = resp.Body
	if req.MaxBodyBytes > 0 {
		bodyReader = io.LimitReader(resp.Body, req.MaxBodyBytes+1)
	}
	respBody, err := io.ReadAll(bodyReader)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if req.MaxBodyBytes > 0 && int64(len(respBody)) > req.MaxBodyBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", req.MaxBodyBytes)
	}

	if req.Credentials != nil {
		secrets.GetTaintRegistry().Taint([]byte(req.Credentials.Password))
		secrets.GetTaintRegistry().Taint([]byte(req.Credentials.Token))
		secrets.GetTaintRegistry().Taint([]byte(req.Credentials.APIKey))
	}

	headers := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	return &connectors.Response{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       respBody,
	}, nil
}

func BuildPath(pathTemplate string, params map[string]interface{}) (string, error) {
	tmpl, err := template.New("path").Parse(pathTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing path template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("executing path template: %w", err)
	}
	return buf.String(), nil
}

func BuildBody(bodyTemplate string, params map[string]interface{}) ([]byte, error) {
	if bodyTemplate == "" {
		return nil, nil
	}
	tmpl, err := template.New("body").Parse(bodyTemplate)
	if err != nil {
		return nil, fmt.Errorf("parsing body template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return nil, fmt.Errorf("executing body template: %w", err)
	}
	return buf.Bytes(), nil
}
