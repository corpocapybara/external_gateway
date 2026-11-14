package email

import (
	"errors"
	"net/smtp"
	"strings"
)

// Auth modes for Config.AuthMode.
const (
	AuthBasic  = "basic"
	AuthOAuth2 = "oauth2"
)

// xoauth2String is the XOAUTH2 initial response shared by IMAP and SMTP:
// "user=<user>\x01auth=Bearer <token>\x01\x01".
func xoauth2String(user, token string) []byte {
	return []byte("user=" + user + "\x01auth=Bearer " + strings.TrimSpace(token) + "\x01\x01")
}

// imapXOAuth2 is a go-sasl client for AUTHENTICATE XOAUTH2 (go-sasl ships only OAUTHBEARER).
type imapXOAuth2 struct {
	user, token string
}

func (a *imapXOAuth2) Start() (string, []byte, error) {
	return "XOAUTH2", xoauth2String(a.user, a.token), nil
}

// Next answers the server's error challenge (a base64 JSON status) with an
// empty response; the server then sends NO.
func (a *imapXOAuth2) Next(challenge []byte) ([]byte, error) {
	return []byte{}, nil
}

// smtpXOAuth2 is a net/smtp Auth for AUTH XOAUTH2.
type smtpXOAuth2 struct {
	user, token string
}

func (a *smtpXOAuth2) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("xoauth2: refusing to send the token without TLS")
	}
	return "XOAUTH2", xoauth2String(a.user, a.token), nil
}

// Next answers a 334 error challenge with a non-nil empty response: net/smtp
// ends AUTH as success on a nil return and never sends the empty line, so the
// server's following 535 would be lost.
func (a *smtpXOAuth2) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		return []byte{}, nil
	}
	return nil, nil
}
