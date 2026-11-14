package server

import (
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/email"
	"github.com/external_gateway/internal/secrets"
)

// emailConfig builds the email connector config of an env: IMAP host Host else
// ImapHost (port 993, TLS), SMTP host SmtpHost else Host (port 587), implicit TLS
// only on SMTP port 465 (else mandatory STARTTLS), XOAUTH2 iff auth.type oauth2.
// rootCAs is nil in production (system roots); only tests set it.
func emailConfig(env *config.Environment, secret []byte, rootCAs *x509.CertPool) *email.Config {
	imapHost := env.Host
	if imapHost == "" {
		imapHost = env.ImapHost
	}
	imapPort := env.ImapPort
	if imapPort == 0 {
		imapPort = 993
	}
	smtpHost := env.SmtpHost
	if smtpHost == "" {
		smtpHost = env.Host
	}
	smtpPort := env.SmtpPort
	if smtpPort == 0 {
		smtpPort = 587
	}
	authMode := email.AuthBasic
	if env.Auth.Type == "oauth2" {
		authMode = email.AuthOAuth2
	}
	return &email.Config{
		ImapHost:        imapHost,
		ImapPort:        imapPort,
		ImapTLS:         true,
		SmtpHost:        smtpHost,
		SmtpPort:        smtpPort,
		SmtpImplicitTLS: smtpPort == 465,
		User:            env.User,
		Password:        string(secret),
		AuthMode:        authMode,
		RootCAs:         rootCAs,
	}
}

// withEmail resolves the env's secret, builds its email config and runs call.
// For an oauth2 env with an oauth block, a rejected login refreshes the access
// token once (refreshOAuthToken: serialised, stores a rotated refresh token,
// flushes the cache) and retries call once with the new token.
func (s *Server) withEmail(env *config.Environment, workspace string, call func(*email.Config) error) error {
	run := func() (*email.Config, error) {
		wsName, secretName := parseSecretRef(env.SecretRef)
		secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace)
		if err != nil {
			return nil, fmt.Errorf("resolving email secret: %w", err)
		}
		cfg := emailConfig(env, secret, s.emailRootCAs)
		return cfg, call(cfg)
	}
	cfg, err := run()
	if err == nil || cfg == nil || !errors.Is(err, email.ErrAuthRejected) ||
		cfg.AuthMode != email.AuthOAuth2 || env.OAuth == nil {
		return err
	}
	if !s.refreshOAuthToken(env, workspace) {
		return fmt.Errorf("%w: OAuth token refresh failed", err)
	}
	_, err = run()
	return err
}
