package email

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strconv"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

type Config struct {
	ImapHost   string
	ImapPort   int
	ImapTLS    bool
	SmtpHost   string
	SmtpPort   int
	SmtpTLS    bool
	User       string
	Password   string
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
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchInternalDate}
	go func() {
		imapClient.Fetch(seqset, items, messages)
	}()

	var result []map[string]interface{}
	for msg := range messages {
		if criteria != "" && msg.Envelope != nil {
			joined := msg.Envelope.Subject + " " + joinAddresses(msg.Envelope.From) + " " + joinAddresses(msg.Envelope.Sender)
			if !containsSubstr(joined, criteria) {
				continue
			}
		}
		entry := map[string]interface{}{
			"uid":    msg.Uid,
			"seq":    msg.SeqNum,
			"date":   msg.Envelope.Date,
			"subject": msg.Envelope.Subject,
			"from":   formatAddresses(msg.Envelope.From),
			"to":     formatAddresses(msg.Envelope.To),
			"flags":  msg.Flags,
		}
		result = append(result, entry)
	}

	return result, nil
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
	if msg == nil {
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
		body, _ := io.ReadAll(literal)
		result["body"] = string(body)
		break
	}

	return result, nil
}

func (c *Connector) Send(cfg *Config, to, subject, body string) error {
	if cfg.SmtpTLS {
		return sendTLS(cfg, to, subject, body)
	}
	return sendPlain(cfg, to, subject, body)
}

func (c *Connector) VerifySMTP(cfg *Config) (string, error) {
	addr := net.JoinHostPort(cfg.SmtpHost, strconv.Itoa(cfg.SmtpPort))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	client, err := smtp.NewClient(conn, cfg.SmtpHost)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()
	if cfg.SmtpTLS {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.SmtpHost}); err != nil {
			return "", fmt.Errorf("starttls: %w", err)
		}
	}
	auth := smtp.PlainAuth("", cfg.User, cfg.Password, cfg.SmtpHost)
	if err := client.Auth(auth); err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}
	return "ok", nil
}

func sendPlain(cfg *Config, to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", cfg.SmtpHost, cfg.SmtpPort)
	auth := smtp.PlainAuth("", cfg.User, cfg.Password, cfg.SmtpHost)
	msg := buildMessage(cfg.User, to, subject, body)
	return smtp.SendMail(addr, auth, cfg.User, []string{to}, []byte(msg))
}

func sendTLS(cfg *Config, to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", cfg.SmtpHost, cfg.SmtpPort)
	tlsCfg := &tls.Config{ServerName: cfg.SmtpHost}

	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}

	client, err := smtp.NewClient(conn, cfg.SmtpHost)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	auth := smtp.PlainAuth("", cfg.User, cfg.Password, cfg.SmtpHost)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("auth: %w", err)
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
	return w.Close()
}

func buildMessage(from, to, subject, body string) string {
	now := time.Now().Format(time.RFC1123Z)
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n\r\n%s\r\n", from, to, subject, now, body)
}

func dialImap(cfg *Config) (*client.Client, error) {
	addr := fmt.Sprintf("%s:%d", cfg.ImapHost, cfg.ImapPort)
	var imapClient *client.Client
	var err error

	if cfg.ImapTLS {
		imapClient, err = client.DialTLS(addr, &tls.Config{ServerName: cfg.ImapHost})
	} else {
		imapClient, err = client.Dial(addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial imap: %w", err)
	}

	if err := imapClient.Login(cfg.User, cfg.Password); err != nil {
		imapClient.Logout()
		return nil, fmt.Errorf("imap login: %w", err)
	}
	return imapClient, nil
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