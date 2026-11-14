// Package formsession logs in to a site with a form POST (email + password)
// and keeps the `connect.sid` session cookie it gets back. Data requests go
// through the server's http-rest path with that cookie (EG0042); this package
// only logs in and caches sessions.
package formsession

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SessionCookieName is the cookie a successful login sets (Express/CodiMD).
const SessionCookieName = "connect.sid"

// loginDrainLimit caps how much of a login response body is read (and discarded).
const loginDrainLimit = 1 << 20

type Connector struct {
	client   *http.Client
	insecure *http.Client
}

// noRedirect stops the client at the first response, so a login's 302 and its
// Set-Cookie are seen, and a data request's redirect to /login is a signal
// rather than a followed hop (same rule as httprest).
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
	return "form-session"
}

// Login POSTs email and password to <baseURL>/login and returns the session
// cookie value. Errors never contain the password, the cookie or the URL.
func (c *Connector) Login(ctx context.Context, baseURL, email, password string, insecure bool) (string, error) {
	loginURL := strings.TrimRight(baseURL, "/") + "/login"
	data := url.Values{}
	data.Set("email", email)
	data.Set("password", password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", errors.New("form-session login: invalid base_url")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := c.client
	if insecure {
		client = c.insecure
	}
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return "", fmt.Errorf("form-session login: %w", urlErr.Err)
		}
		return "", fmt.Errorf("form-session login: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, loginDrainLimit))

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("form-session login refused (status %d)", resp.StatusCode)
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("form-session login failed (status %d)", resp.StatusCode)
	case resp.StatusCode >= 300 && resp.StatusCode < 400 && NeedsLogin(resp.StatusCode, resp.Header.Get("Location")):
		return "", errors.New("form-session login refused")
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == SessionCookieName && cookie.Value != "" {
			return cookie.Value, nil
		}
	}
	return "", errors.New("form-session login: no session cookie")
}

// NeedsLogin reports whether an upstream answer means the session is gone: a
// 401, or a redirect whose Location path ends in /login (absolute or relative,
// query ignored). A 403 or a redirect elsewhere is an ordinary answer.
func NeedsLogin(status int, location string) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	if status < 300 || status >= 400 || location == "" {
		return false
	}
	u, err := url.Parse(location)
	if err != nil {
		return false
	}
	return strings.HasSuffix(u.Path, "/login")
}
