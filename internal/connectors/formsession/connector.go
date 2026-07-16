package formsession

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/external_gateway/internal/connectors"
)

type Connector struct {
	client      *http.Client
	sessionCookie string
	baseURL     string
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func NewConnector() *Connector {
	return &Connector{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Connector) Name() string {
	return "form-session"
}

func (c *Connector) Login(ctx context.Context, baseURL, email, password string) error {
	loginURL := fmt.Sprintf("%s/login", baseURL)
	data := url.Values{}
	data.Set("email", email)
	data.Set("password", password)

	req, err := http.NewRequestWithContext(ctx, "POST", loginURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("creating login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing login: %w", err)
	}
	defer resp.Body.Close()

	for _, cookie := range resp.Cookies() {
		if cookie.Name == "connect.sid" {
			c.sessionCookie = cookie.Value
			c.baseURL = baseURL
			return nil
		}
	}

	return fmt.Errorf("session cookie not found in response")
}

func (c *Connector) Execute(ctx context.Context, req *connectors.Request) (*connectors.Response, error) {
	if c.sessionCookie == "" {
		return nil, fmt.Errorf("not logged in - call Login first")
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.Path, bytes.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.AddCookie(&http.Cookie{Name: "connect.sid", Value: c.sessionCookie})
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
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

func UnmarshalJSON[T any](data []byte) (T, error) {
	var result T
	err := json.Unmarshal(data, &result)
	return result, err
}
