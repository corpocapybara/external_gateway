package email

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

type Config struct {
	ImapHost string
	ImapPort int
	ImapTLS  bool
	SmtpHost string
	SmtpPort int
	// SmtpImplicitTLS dials TLS from the first byte (port 465); otherwise the
	// connection starts in plain text and must complete STARTTLS before AUTH.
	SmtpImplicitTLS bool
	User            string
	// Password is the account password (basic) or the OAuth access token (oauth2).
	Password string
	// AuthMode is AuthBasic (default, also when empty) or AuthOAuth2 (XOAUTH2).
	AuthMode string
	// RootCAs verifies the IMAP and SMTP servers' certificates; nil uses the
	// system roots. Only tests set it.
	RootCAs *x509.CertPool
}

// ErrAuthRejected marks an error where the server answered a login attempt and
// refused the credentials (IMAP NO/BAD with the connection kept, SMTP 535).
var ErrAuthRejected = errors.New("email: authentication rejected")

func (cfg *Config) oauth2() bool { return cfg.AuthMode == AuthOAuth2 }

func (cfg *Config) tlsConfig(host string) *tls.Config {
	return &tls.Config{ServerName: host, RootCAs: cfg.RootCAs}
}

type Connector struct{}

func NewConnector() *Connector {
	return &Connector{}
}

func (c *Connector) Name() string {
	return "email"
}

func (c *Connector) ListMailboxes(cfg *Config) ([]map[string]interface{}, error) {
	imapClient, err := dialImap(cfg)
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	mailboxes := make(chan *imap.MailboxInfo, 10)
	done := make(chan error, 1)
	go func() {
		done <- imapClient.List("", "*", mailboxes)
	}()

	var result []map[string]interface{}
	for m := range mailboxes {
		result = append(result, map[string]interface{}{
			"name":  m.Name,
			"attrs": m.Attributes,
		})
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("listing mailboxes: %w", err)
	}
	return result, nil
}

func (c *Connector) Search(cfg *Config, mailbox, criteria string) ([]map[string]interface{}, error) {
	imapClient, err := dialImap(cfg)
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	mbox, err := imapClient.Select(mailbox, false)
	if err != nil {
		return nil, fmt.Errorf("selecting %s: %w", mailbox, err)
	}

	if mbox.Messages == 0 {
		return nil, nil
	}

	seqset := new(imap.SeqSet)
	from := uint32(1)
	if mbox.Messages > 50 {
		from = mbox.Messages - 49
	}
	seqset.AddRange(from, mbox.Messages)

	messages := make(chan *imap.Message, 10)
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchFlags, imap.FetchInternalDate}
	done := make(chan error, 1)
	go func() {
		done <- imapClient.Fetch(seqset, items, messages)
	}()

	var result []map[string]interface{}
	for msg := range messages {
		entry, ok := searchEntry(msg, criteria)
		if !ok {
			continue
		}
		result = append(result, entry)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("fetching %s: %w", mailbox, err)
	}

	return result, nil
}

// searchEntry builds the result row for one fetched message. It reports false
// when the message has no envelope or does not match criteria.
func searchEntry(msg *imap.Message, criteria string) (map[string]interface{}, bool) {
	if msg == nil || msg.Envelope == nil {
		return nil, false
	}
	if criteria != "" {
		joined := msg.Envelope.Subject + " " + joinAddresses(msg.Envelope.From) + " " + joinAddresses(msg.Envelope.Sender)
		if !containsSubstr(joined, criteria) {
			return nil, false
		}
	}
	return map[string]interface{}{
		"uid":     msg.Uid,
		"seq":     msg.SeqNum,
		"date":    msg.Envelope.Date,
		"subject": msg.Envelope.Subject,
		"from":    formatAddresses(msg.Envelope.From),
		"to":      formatAddresses(msg.Envelope.To),
		"flags":   msg.Flags,
	}, true
}

// maxBodyBytes caps how much of a message body ReadMessage returns.
const maxBodyBytes = 10 << 20

// readBody reads at most limit bytes from r and reports whether more data followed.
func readBody(r io.Reader, limit int64) (string, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(data)) > limit {
		return string(data[:limit]), true, err
	}
	return string(data), false, err
}

func (c *Connector) ReadMessage(cfg *Config, mailbox string, uid uint32) (map[string]interface{}, error) {
	imapClient, err := dialImap(cfg)
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	if _, err := imapClient.Select(mailbox, false); err != nil {
		return nil, fmt.Errorf("selecting %s: %w", mailbox, err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchInternalDate, section.FetchItem()}
	messages := make(chan *imap.Message, 1)
	go func() {
		imapClient.UidFetch(seqset, items, messages)
	}()

	msg := <-messages
	if msg == nil || msg.Envelope == nil {
		return nil, fmt.Errorf("message uid %d not found", uid)
	}

	result := map[string]interface{}{
		"uid":     msg.Uid,
		"seq":     msg.SeqNum,
		"subject": msg.Envelope.Subject,
		"from":    formatAddresses(msg.Envelope.From),
		"to":      formatAddresses(msg.Envelope.To),
		"cc":      formatAddresses(msg.Envelope.Cc),
		"date":    msg.Envelope.Date,
		"flags":   msg.Flags,
	}

	for _, literal := range msg.Body {
		body, truncated, err := readBody(literal, maxBodyBytes)
		if err != nil {
			return nil, fmt.Errorf("reading body: %w", err)
		}
		result["body"] = body
		if truncated {
			result["body_truncated"] = true
		}
		break
	}

	return result, nil
}

// checkHeaderValue rejects CR, LF and other control characters (tab allowed).
func checkHeaderValue(name, v string) error {
	for _, r := range v {
		if r != '\t' && unicode.IsControl(r) {
			return fmt.Errorf("invalid %s: control characters not allowed", name)
		}
	}
	return nil
}

// ParseRecipient validates that to is exactly one bare address (no display
// name, no list) and returns it.
func ParseRecipient(to string) (string, error) {
	if err := checkHeaderValue("recipient", to); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(to)
	addr, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid recipient: %w", err)
	}
	if addr.Address != trimmed {
		return "", fmt.Errorf("invalid recipient: must be a single bare address")
	}
	return addr.Address, nil
}

func (c *Connector) Send(cfg *Config, to, subject, body string) error {
	to, err := ParseRecipient(to)
	if err != nil {
		return err
	}
	if err := checkHeaderValue("subject", subject); err != nil {
		return err
	}
	if err := checkHeaderValue("from", cfg.User); err != nil {
		return err
	}
	return sendSMTP(cfg, to, subject, body)
}

// VerifySMTP connects and authenticates without sending.
func (c *Connector) VerifySMTP(cfg *Config) (string, error) {
	client, err := dialSMTP(cfg)
	if err != nil {
		return "", err
	}
	defer client.Close()
	if err := smtpAuth(client, cfg); err != nil {
		return "", err
	}
	return "ok", nil
}

// dialSMTP connects to the SMTP server: implicit TLS when SmtpImplicitTLS,
// otherwise plain text upgraded with STARTTLS, which is mandatory (no AUTH is
// ever sent in the clear). The connection gets a deadline so a stalled server
// cannot hang the gateway.
func dialSMTP(cfg *Config) (*smtp.Client, error) {
	addr := net.JoinHostPort(cfg.SmtpHost, strconv.Itoa(cfg.SmtpPort))
	dialer := &net.Dialer{Timeout: dialTimeout}
	var conn net.Conn
	var err error
	if cfg.SmtpImplicitTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, cfg.tlsConfig(cfg.SmtpHost))
		if err != nil {
			return nil, fmt.Errorf("smtp tls dial: %w", err)
		}
	} else {
		conn, err = dialer.Dial("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("smtp dial: %w", err)
		}
	}
	conn.SetDeadline(time.Now().Add(commandTimeout))
	client, err := smtp.NewClient(conn, cfg.SmtpHost)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smtp client: %w", err)
	}
	if !cfg.SmtpImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			client.Close()
			return nil, errors.New("smtp server does not offer STARTTLS")
		}
		if err := client.StartTLS(cfg.tlsConfig(cfg.SmtpHost)); err != nil {
			client.Close()
			return nil, fmt.Errorf("smtp starttls: %w", err)
		}
	}
	return client, nil
}

// smtpAuth authenticates with AUTH XOAUTH2 (oauth2) or AUTH PLAIN (basic). A
// 535 reply is wrapped as ErrAuthRejected.
func smtpAuth(client *smtp.Client, cfg *Config) error {
	var auth smtp.Auth
	if cfg.oauth2() {
		auth = &smtpXOAuth2{user: cfg.User, token: cfg.Password}
	} else {
		auth = smtp.PlainAuth("", cfg.User, cfg.Password, cfg.SmtpHost)
	}
	if err := client.Auth(auth); err != nil {
		var tpErr *textproto.Error
		if errors.As(err, &tpErr) && tpErr.Code == 535 {
			return fmt.Errorf("smtp auth: %w: %v", ErrAuthRejected, err)
		}
		return fmt.Errorf("smtp auth: %w", err)
	}
	return nil
}

func sendSMTP(cfg *Config, to, subject, body string) error {
	client, err := dialSMTP(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := smtpAuth(client, cfg); err != nil {
		return err
	}
	if err := client.Mail(cfg.User); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	msg := buildMessage(cfg.User, to, subject, body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("data close: %w", err)
	}
	client.Quit()
	return nil
}

func buildMessage(from, to, subject, body string) string {
	now := time.Now().Format(time.RFC1123Z)
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n\r\n%s\r\n", from, to, mime.QEncoding.Encode("UTF-8", subject), now, body)
}

const (
	dialTimeout    = 10 * time.Second
	commandTimeout = 60 * time.Second
)

func dialImap(cfg *Config) (*client.Client, error) {
	addr := net.JoinHostPort(cfg.ImapHost, strconv.Itoa(cfg.ImapPort))
	dialer := &net.Dialer{Timeout: dialTimeout}
	var imapClient *client.Client
	var err error

	if cfg.ImapTLS {
		imapClient, err = client.DialWithDialerTLS(dialer, addr, cfg.tlsConfig(cfg.ImapHost))
	} else {
		imapClient, err = client.DialWithDialer(dialer, addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial imap: %w", err)
	}
	// Per-command deadline so a stalled server cannot hang the gateway.
	imapClient.Timeout = commandTimeout

	if !cfg.ImapTLS {
		// Upgrade with STARTTLS when offered; otherwise keep the existing plaintext behaviour.
		ok, err := imapClient.SupportStartTLS()
		if err != nil {
			imapClient.Terminate()
			return nil, fmt.Errorf("imap capability: %w", err)
		}
		if ok {
			if err := imapClient.StartTLS(cfg.tlsConfig(cfg.ImapHost)); err != nil {
				imapClient.Terminate()
				return nil, fmt.Errorf("imap starttls: %w", err)
			}
		} else if cfg.oauth2() {
			// Never send a bearer token in the clear.
			imapClient.Logout()
			return nil, errors.New("imap: refusing XOAUTH2 without TLS")
		}
	}

	var authErr error
	if cfg.oauth2() {
		authErr = imapClient.Authenticate(&imapXOAuth2{user: cfg.User, token: cfg.Password})
	} else {
		authErr = imapClient.Login(cfg.User, cfg.Password)
	}
	if authErr != nil {
		rejected := imapAuthRejected(imapClient, authErr)
		imapClient.Logout()
		if rejected {
			return nil, fmt.Errorf("imap login: %w: %v", ErrAuthRejected, authErr)
		}
		return nil, fmt.Errorf("imap login: %w", authErr)
	}
	return imapClient, nil
}

// imapAuthRejected reports whether a Login/Authenticate error is the server
// refusing the credentials. go-imap v1.2.1 returns NO, BAD and its own
// closed-connection error as untyped errors, so this decides by connection
// state: the server answered and kept the connection (LoggedOut() still open;
// go-imap returns its closed error only after that channel closed), and the
// error is no network, EOF or LOGINDISABLED error. A client-side SASL error that
// leaves the connection open also counts (costs at most one token refresh).
func imapAuthRejected(c *client.Client, err error) bool {
	select {
	case <-c.LoggedOut():
		return false
	default:
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, client.ErrLoginDisabled) {
		return false
	}
	return true
}

func formatAddresses(addrs []*imap.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		if a.MailboxName != "" && a.HostName != "" {
			parts[i] = a.MailboxName + "@" + a.HostName
		} else if a.MailboxName != "" {
			parts[i] = a.MailboxName
		}
	}
	return joinStrings(parts, "; ")
}

func joinAddresses(addrs []*imap.Address) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.MailboxName + "@" + a.HostName
	}
	return joinStrings(parts, " ")
}

func joinStrings(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}

func containsSubstr(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	upperS := toUpper(s)
	upperSub := toUpper(substr)
	return contains(upperS, upperSub)
}

func toUpper(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		b[i] = c
	}
	return string(b)
}

func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}