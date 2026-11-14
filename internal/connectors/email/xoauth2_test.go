package email

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"

	"github.com/external_gateway/internal/connectors/email/emailtest"
)

// testTLS is the package's one generated certificate (SAN 127.0.0.1).
var testTLS *emailtest.TLS

func TestMain(m *testing.M) {
	var err error
	if testTLS, err = emailtest.NewTLS(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

const testUser = "me@example.com"

func imapCfg(port int, mode, password string) *Config {
	return &Config{ImapHost: "127.0.0.1", ImapPort: port, ImapTLS: true, User: testUser,
		Password: password, AuthMode: mode, RootCAs: testTLS.Roots}
}

func smtpCfg(port int, implicitTLS bool, mode, password string) *Config {
	return &Config{SmtpHost: "127.0.0.1", SmtpPort: port, SmtpImplicitTLS: implicitTLS, User: testUser,
		Password: password, AuthMode: mode, RootCAs: testTLS.Roots}
}

func wantXOAuth2(token string) string {
	return "user=" + testUser + "\x01auth=Bearer " + token + "\x01\x01"
}

// assertNoToken checks that an error text leaks neither the token nor its XOAUTH2 string.
func assertNoToken(t *testing.T, err error, token string) {
	t.Helper()
	ir := wantXOAuth2(token)
	for _, s := range []string{token, ir, base64.StdEncoding.EncodeToString([]byte(ir))} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error text leaks the token: %q", err.Error())
		}
	}
}

func TestImapXOAuth2Login(t *testing.T) {
	srv := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: testTLS.Server, ValidToken: "tok"})
	boxes, err := NewConnector().ListMailboxes(imapCfg(srv.Port, AuthOAuth2, "tok\n"))
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(boxes) == 0 {
		t.Fatal("no mailboxes listed after XOAUTH2 login")
	}
	irs := srv.IRs()
	if len(irs) != 1 || string(irs[0]) != wantXOAuth2("tok") {
		t.Fatalf("XOAUTH2 initial responses = %q, want [%q]", irs, wantXOAuth2("tok"))
	}
	if logins := srv.Logins(); len(logins) != 0 {
		t.Fatalf("LOGIN must not be sent in oauth2 mode, got %v", logins)
	}
}

func TestImapBasicLoginUnchanged(t *testing.T) {
	srv := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: testTLS.Server})
	cfg := imapCfg(srv.Port, "", emailtest.MemPassword)
	cfg.User = emailtest.MemUser
	if _, err := NewConnector().ListMailboxes(cfg); err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	logins := srv.Logins()
	if len(logins) != 1 || logins[0] != [2]string{emailtest.MemUser, emailtest.MemPassword} {
		t.Fatalf("LOGIN commands = %v, want one with user and password", logins)
	}
	if len(srv.IRs()) != 0 {
		t.Fatal("basic mode must not AUTHENTICATE XOAUTH2")
	}
}

func TestImapXOAuth2Rejected(t *testing.T) {
	srv := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: testTLS.Server, ValidToken: "good"})
	_, err := NewConnector().ListMailboxes(imapCfg(srv.Port, AuthOAuth2, "bad-token\n"))
	if !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected, got %v", err)
	}
	assertNoToken(t, err, "bad-token")
	// First Next: the IR, answered with the error challenge; second Next (after
	// the client's empty response): the refusal.
	if n := srv.NextCalls(); n != 2 {
		t.Fatalf("XOAUTH2 Next calls = %d, want 2 (challenge, then NO)", n)
	}
}

func TestImapXOAuth2RefusedWithoutTLS(t *testing.T) {
	srv := emailtest.StartIMAP(t, emailtest.IMAPOptions{ValidToken: "tok"})
	cfg := imapCfg(srv.Port, AuthOAuth2, "tok")
	cfg.ImapTLS = false
	_, err := NewConnector().ListMailboxes(cfg)
	if err == nil || !strings.Contains(err.Error(), "refusing XOAUTH2 without TLS") {
		t.Fatalf("want refusal without TLS, got %v", err)
	}
	if n := srv.NextCalls(); n != 0 {
		t.Fatalf("XOAUTH2 Next called %d times; no AUTHENTICATE may be sent without TLS", n)
	}
}

func TestDialErrorNotAuthRejected(t *testing.T) {
	port := emailtest.ClosedPort(t)
	_, err := NewConnector().ListMailboxes(imapCfg(port, AuthOAuth2, "tok"))
	if err == nil || errors.Is(err, ErrAuthRejected) {
		t.Fatalf("IMAP dial error must not be ErrAuthRejected, got %v", err)
	}
	_, err = NewConnector().VerifySMTP(smtpCfg(port, false, AuthOAuth2, "tok"))
	if err == nil || errors.Is(err, ErrAuthRejected) {
		t.Fatalf("SMTP dial error must not be ErrAuthRejected, got %v", err)
	}
}

// TestImapConnectionClosedNotAuthRejected: the server drops the connection after
// reading AUTHENTICATE; whatever error go-imap returns is not a rejection.
func TestImapConnectionClosedNotAuthRejected(t *testing.T) {
	l, err := tls.Listen("tcp", "127.0.0.1:0", testTLS.Server)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	gotAuth := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(10 * time.Second))
		c.Write([]byte("* OK [CAPABILITY IMAP4rev1 AUTH=XOAUTH2 SASL-IR] ready\r\n"))
		line, _ := bufio.NewReader(c).ReadString('\n')
		gotAuth <- line
	}()
	port := l.Addr().(*net.TCPAddr).Port
	_, err = NewConnector().ListMailboxes(imapCfg(port, AuthOAuth2, "tok"))
	if err == nil || errors.Is(err, ErrAuthRejected) {
		t.Fatalf("closed connection must not be ErrAuthRejected, got %v", err)
	}
	if line := <-gotAuth; !strings.Contains(line, "AUTHENTICATE XOAUTH2") {
		t.Fatalf("server read %q, want the AUTHENTICATE line", line)
	}
}

func TestSmtpStartTLSThenXOAuth2(t *testing.T) {
	srv := emailtest.StartSMTP(t, emailtest.SMTPOptions{TLS: testTLS.Server, ValidToken: "tok"})
	if err := NewConnector().Send(smtpCfg(srv.Port, false, AuthOAuth2, "tok\n"), "a@example.com", "Hi", "body text"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	lines := srv.Lines()
	startTLS, auth := -1, -1
	for i, l := range lines {
		if l == "STARTTLS" && startTLS < 0 {
			startTLS = i
		}
		if strings.HasPrefix(l, "AUTH ") && auth < 0 {
			auth = i
		}
	}
	if startTLS < 0 || auth < 0 || startTLS > auth {
		t.Fatalf("STARTTLS must precede AUTH: %q", lines)
	}
	if got := srv.XOAuth2(); len(got) != 1 || got[0] != wantXOAuth2("tok") {
		t.Fatalf("XOAUTH2 = %q, want [%q]", got, wantXOAuth2("tok"))
	}
	if tlsAuth := srv.AuthOverTLS(); len(tlsAuth) != 1 || !tlsAuth[0] {
		t.Fatalf("AUTH over TLS = %v", tlsAuth)
	}
	from, to, data := srv.Message()
	if len(from) != 1 || from[0] != "MAIL FROM:<"+testUser+">" {
		t.Fatalf("MAIL FROM = %q", from)
	}
	if len(to) != 1 || to[0] != "RCPT TO:<a@example.com>" {
		t.Fatalf("RCPT TO = %q", to)
	}
	if len(data) != 1 || !strings.Contains(data[0], "Subject: Hi\r\n") || !strings.Contains(data[0], "\r\n\r\nbody text\r\n") ||
		!strings.Contains(data[0], "To: a@example.com\r\n") {
		t.Fatalf("DATA = %q", data)
	}
}

func TestSmtpNoStartTLSRefused(t *testing.T) {
	srv := emailtest.StartSMTP(t, emailtest.SMTPOptions{TLS: testTLS.Server, NoStartTLS: true, ValidToken: "tok"})
	err := NewConnector().Send(smtpCfg(srv.Port, false, AuthOAuth2, "tok"), "a@example.com", "Hi", "b")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("want STARTTLS error, got %v", err)
	}
	for _, l := range srv.Lines() {
		if strings.HasPrefix(l, "AUTH") {
			t.Fatalf("AUTH sent without STARTTLS: %q", srv.Lines())
		}
	}
}

func TestSmtpImplicitTLS(t *testing.T) {
	srv := emailtest.StartSMTP(t, emailtest.SMTPOptions{TLS: testTLS.Server, ImplicitTLS: true, ValidToken: "tok"})
	if _, err := NewConnector().VerifySMTP(smtpCfg(srv.Port, true, AuthOAuth2, "tok")); err != nil {
		t.Fatalf("VerifySMTP: %v", err)
	}
	for _, l := range srv.Lines() {
		if l == "STARTTLS" {
			t.Fatal("implicit TLS must not issue STARTTLS")
		}
	}
	if tlsAuth := srv.AuthOverTLS(); len(tlsAuth) != 1 || !tlsAuth[0] {
		t.Fatalf("AUTH over TLS = %v", tlsAuth)
	}
	if got := srv.XOAuth2(); len(got) != 1 || got[0] != wantXOAuth2("tok") {
		t.Fatalf("XOAUTH2 = %q", got)
	}
}

func TestSmtpBasicPlainUnchanged(t *testing.T) {
	srv := emailtest.StartSMTP(t, emailtest.SMTPOptions{TLS: testTLS.Server, PlainUser: testUser, PlainPassword: "pw"})
	if _, err := NewConnector().VerifySMTP(smtpCfg(srv.Port, false, "", "pw")); err != nil {
		t.Fatalf("VerifySMTP: %v", err)
	}
	if got := srv.Plain(); len(got) != 1 || got[0] != "\x00"+testUser+"\x00pw" {
		t.Fatalf("AUTH PLAIN = %q", got)
	}
	if len(srv.XOAuth2()) != 0 {
		t.Fatal("basic mode must not send XOAUTH2")
	}
}

func TestSmtpXOAuth2Rejected(t *testing.T) {
	srv := emailtest.StartSMTP(t, emailtest.SMTPOptions{TLS: testTLS.Server, ValidToken: "good"})
	_, err := NewConnector().VerifySMTP(smtpCfg(srv.Port, false, AuthOAuth2, "bad-token\n"))
	if !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected, got %v", err)
	}
	assertNoToken(t, err, "bad-token")
	lines := srv.Lines()
	auth := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "AUTH XOAUTH2") {
			auth = i
		}
	}
	if auth < 0 || auth+1 >= len(lines) || lines[auth+1] != "" {
		t.Fatalf("client must answer the 334 challenge with an empty line before the 535: %q", lines)
	}
}

func TestSmtpXOAuth2AuthRequiresTLS(t *testing.T) {
	a := &smtpXOAuth2{user: testUser, token: "secret-token\n"}
	_, _, err := a.Start(&smtp.ServerInfo{Name: "127.0.0.1", TLS: false, Auth: []string{"XOAUTH2"}})
	if err == nil {
		t.Fatal("Start without TLS must fail")
	}
	assertNoToken(t, err, "secret-token")
	mech, ir, err := a.Start(&smtp.ServerInfo{Name: "127.0.0.1", TLS: true, Auth: []string{"XOAUTH2"}})
	if err != nil || mech != "XOAUTH2" || string(ir) != wantXOAuth2("secret-token") {
		t.Fatalf("Start with TLS = %q, %q, %v", mech, ir, err)
	}
}

// seedInbox replaces INBOX with three messages and expunges the first, so the
// remaining UIDs (2, 3) differ from their sequence numbers (1, 2).
func seedInbox(t *testing.T, be *memory.Backend) {
	t.Helper()
	user, err := be.Login(nil, emailtest.MemUser, emailtest.MemPassword)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := user.GetMailbox("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	inbox := mb.(*memory.Mailbox)
	inbox.Messages = nil
	for _, subj := range []string{"first", "second", "third"} {
		body := "From: a@example.com\r\nTo: me@example.com\r\nSubject: " + subj + "\r\n\r\nbody " + subj + "\r\n"
		if err := inbox.CreateMessage(nil, time.Now(), strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	inbox.Messages[0].Flags = []string{imap.DeletedFlag}
	if err := inbox.Expunge(); err != nil {
		t.Fatal(err)
	}
}

func TestSearchReturnsUID(t *testing.T) {
	srv := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: testTLS.Server, ValidToken: "tok"})
	seedInbox(t, srv.Backend)
	c := NewConnector()
	cfg := imapCfg(srv.Port, AuthOAuth2, "tok")
	msgs, err := c.Search(cfg, "INBOX", "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := map[uint32]string{2: "second", 3: "third"}
	if len(msgs) != len(want) {
		t.Fatalf("Search returned %d messages, want %d: %v", len(msgs), len(want), msgs)
	}
	for _, m := range msgs {
		uid, _ := m["uid"].(uint32)
		subj, ok := want[uid]
		if !ok || m["subject"] != subj {
			t.Fatalf("search row uid=%v subject=%v, want one of %v", m["uid"], m["subject"], want)
		}
		if seq, _ := m["seq"].(uint32); seq == uid {
			t.Fatalf("uid %d equals seq; the seed must make them differ", uid)
		}
		read, err := c.ReadMessage(cfg, "INBOX", uid)
		if err != nil {
			t.Fatalf("ReadMessage(%d): %v", uid, err)
		}
		if read["subject"] != subj {
			t.Fatalf("ReadMessage(%d) subject = %v, want %s", uid, read["subject"], subj)
		}
	}
}

func TestSearchEmptyMailbox(t *testing.T) {
	srv := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: testTLS.Server, ValidToken: "tok"})
	user, err := srv.Backend.Login(nil, emailtest.MemUser, emailtest.MemPassword)
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := user.GetMailbox("INBOX")
	mb.(*memory.Mailbox).Messages = nil
	msgs, err := NewConnector().Search(imapCfg(srv.Port, AuthOAuth2, "tok"), "INBOX", "")
	if err != nil || len(msgs) != 0 {
		t.Fatalf("empty mailbox: got %v, %v; want no messages and no error", msgs, err)
	}
}
