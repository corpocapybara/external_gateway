// Package emailtest holds in-process IMAP and SMTP fakes on 127.0.0.1 for the
// email connector's tests and the server's email tests. Test-only: nothing in
// production imports it.
package emailtest

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
	"github.com/emersion/go-sasl"
)

// MemUser and MemPassword are the memory backend's only account.
const (
	MemUser     = "username"
	MemPassword = "password"
)

// ErrorChallenge is the base64 JSON status an XOAUTH2 server sends before refusing.
var ErrorChallenge = []byte(`{"status":"401","schemes":"bearer","scope":"https://mail.example/"}`)

// TLS holds one self-signed ECDSA certificate for 127.0.0.1.
type TLS struct {
	Server *tls.Config    // for the fakes
	Roots  *x509.CertPool // for the client (email.Config.RootCAs)
}

// NewTLS generates a certificate whose SAN is 127.0.0.1. Call it once per test
// package (keygen is the slow part) and share the result.
func NewTLS() (*TLS, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &TLS{
		Server: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		Roots:  roots,
	}, nil
}

// ClosedPort returns a 127.0.0.1 port that refuses connections (listened on, then closed).
func ClosedPort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// ParseXOAuth2 splits an XOAUTH2 initial response into user and token.
func ParseXOAuth2(ir []byte) (user, token string, ok bool) {
	s := string(ir)
	if !strings.HasPrefix(s, "user=") || !strings.HasSuffix(s, "\x01\x01") {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimSuffix(s, "\x01\x01"), "\x01", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[1], "auth=Bearer ") {
		return "", "", false
	}
	return strings.TrimPrefix(parts[0], "user="), strings.TrimPrefix(parts[1], "auth=Bearer "), true
}

// IMAPServer is a go-imap server over the memory backend with an XOAUTH2
// mechanism that accepts only ValidToken.
type IMAPServer struct {
	Port    int
	Backend *memory.Backend
	opts    IMAPOptions

	mu        sync.Mutex
	irs       [][]byte
	nextCalls int
	logins    [][2]string
}

// IMAPOptions configures StartIMAP.
type IMAPOptions struct {
	TLS        *tls.Config // implicit TLS listener; nil = plain text, no STARTTLS, insecure auth allowed
	ValidToken string      // the only XOAUTH2 token accepted
}

// IRs returns the XOAUTH2 initial responses received (one per AUTHENTICATE).
func (s *IMAPServer) IRs() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.irs...)
}

// NextCalls counts calls of the XOAUTH2 mechanism's Next.
func (s *IMAPServer) NextCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextCalls
}

// Logins returns the LOGIN commands received as (user, password).
func (s *IMAPServer) Logins() [][2]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]string(nil), s.logins...)
}

// AuthAttempts is the number of LOGIN plus AUTHENTICATE XOAUTH2 commands received.
func (s *IMAPServer) AuthAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.irs) + len(s.logins)
}

// recordingBackend counts LOGIN commands before passing them to the memory backend.
type recordingBackend struct {
	*memory.Backend
	s *IMAPServer
}

func (b recordingBackend) Login(info *imap.ConnInfo, user, pass string) (backend.User, error) {
	b.s.mu.Lock()
	b.s.logins = append(b.s.logins, [2]string{user, pass})
	b.s.mu.Unlock()
	return b.Backend.Login(info, user, pass)
}

// xoauth2Server is one AUTHENTICATE XOAUTH2 exchange. go-imap's server passes
// the SASL-IR on the first Next; after the error challenge it skips the
// client's empty line and calls Next again with the same IR, so the second call
// is told apart by count, not by content.
type xoauth2Server struct {
	s     *IMAPServer
	conn  server.Conn
	calls int
}

func (x *xoauth2Server) Next(response []byte) ([]byte, bool, error) {
	x.s.mu.Lock()
	x.s.nextCalls++
	x.calls++
	if x.calls == 1 {
		x.s.irs = append(x.s.irs, append([]byte(nil), response...))
	}
	x.s.mu.Unlock()
	if x.calls > 1 {
		return nil, false, errors.New("AUTHENTICATE failed.")
	}
	_, token, ok := ParseXOAuth2(response)
	if !ok || token != x.s.validToken() {
		return ErrorChallenge, false, nil
	}
	user, err := x.s.Backend.Login(nil, MemUser, MemPassword)
	if err != nil {
		return nil, false, err
	}
	ctx := x.conn.Context()
	ctx.User = user
	ctx.State = imap.AuthenticatedState
	return nil, true, nil
}

func (s *IMAPServer) validToken() string { return s.opts.ValidToken }

// StartIMAP serves IMAP on 127.0.0.1 until the test ends. The server never
// closes a connection after a NO, so a rejected client sees the connection open.
func StartIMAP(t testing.TB, opts IMAPOptions) *IMAPServer {
	t.Helper()
	is := &IMAPServer{Backend: memory.New(), opts: opts}
	srv := server.New(recordingBackend{is.Backend, is})
	srv.ErrorLog = discardLog{}
	srv.AllowInsecureAuth = opts.TLS == nil
	srv.EnableAuth("XOAUTH2", func(conn server.Conn) sasl.Server {
		return &xoauth2Server{s: is, conn: conn}
	})
	var l net.Listener
	var err error
	if opts.TLS != nil {
		l, err = tls.Listen("tcp", "127.0.0.1:0", opts.TLS)
	} else {
		l, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	is.Port = l.Addr().(*net.TCPAddr).Port
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return is
}

type discardLog struct{}

func (discardLog) Printf(string, ...interface{}) {}
func (discardLog) Println(...interface{})        {}

// SMTPServer is a line-based fake SMTP server recording every command.
type SMTPServer struct {
	Port int
	opts SMTPOptions

	mu       sync.Mutex
	lines    []string
	xoauth2  []string
	plain    []string
	authTLS  []bool
	mailFrom []string
	rcptTo   []string
	data     []string
}

// SMTPOptions configures StartSMTP.
type SMTPOptions struct {
	TLS           *tls.Config // certificate for STARTTLS and implicit TLS
	ImplicitTLS   bool        // TLS from the first byte
	NoStartTLS    bool        // do not offer STARTTLS
	ValidToken    string      // the only XOAUTH2 token accepted
	PlainUser     string      // AUTH PLAIN credentials accepted
	PlainPassword string
}

// Lines returns every command line received, in order (DATA content excluded).
func (s *SMTPServer) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// XOAuth2 returns the decoded XOAUTH2 initial responses received.
func (s *SMTPServer) XOAuth2() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.xoauth2...)
}

// Plain returns the decoded AUTH PLAIN responses received.
func (s *SMTPServer) Plain() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.plain...)
}

// AuthOverTLS reports, per AUTH command, whether the connection was TLS.
func (s *SMTPServer) AuthOverTLS() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.authTLS...)
}

// Message returns the MAIL FROM, RCPT TO and DATA of the messages received.
func (s *SMTPServer) Message() (from, to, data []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.mailFrom...), append([]string(nil), s.rcptTo...), append([]string(nil), s.data...)
}

// StartSMTP serves SMTP on 127.0.0.1 until the test ends.
func StartSMTP(t testing.TB, opts SMTPOptions) *SMTPServer {
	t.Helper()
	ss := &SMTPServer{opts: opts}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ss.Port = l.Addr().(*net.TCPAddr).Port
	var wg sync.WaitGroup
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				ss.serve(c)
			}()
		}
	}()
	t.Cleanup(func() { l.Close(); wg.Wait() })
	return ss
}

func (s *SMTPServer) record(f func()) {
	s.mu.Lock()
	f()
	s.mu.Unlock()
}

func (s *SMTPServer) serve(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	isTLS := false
	if s.opts.ImplicitTLS {
		tc := tls.Server(c, s.opts.TLS)
		if err := tc.Handshake(); err != nil {
			return
		}
		c, isTLS = tc, true
	}
	r := bufio.NewReader(c)
	reply := func(lines ...string) {
		c.Write([]byte(strings.Join(lines, "\r\n") + "\r\n"))
	}
	readLine := func() (string, bool) {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", false
		}
		return strings.TrimRight(line, "\r\n"), true
	}
	reply("220 fake ESMTP")
	for {
		line, ok := readLine()
		if !ok {
			return
		}
		s.record(func() { s.lines = append(s.lines, line) })
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO", "HELO":
			lines := []string{"250-fake"}
			if !isTLS && !s.opts.NoStartTLS {
				lines = append(lines, "250-STARTTLS")
			}
			lines = append(lines, "250 AUTH PLAIN XOAUTH2")
			reply(lines...)
		case "STARTTLS":
			if isTLS || s.opts.NoStartTLS {
				reply("502 not supported")
				continue
			}
			reply("220 ready")
			tc := tls.Server(c, s.opts.TLS)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, isTLS = tc, true
			r = bufio.NewReader(c)
		case "AUTH":
			fields := strings.Fields(line)
			if len(fields) < 2 {
				reply("501 syntax")
				continue
			}
			var ir []byte
			if len(fields) > 2 {
				ir, _ = base64.StdEncoding.DecodeString(fields[2])
			}
			tlsNow := isTLS
			s.record(func() { s.authTLS = append(s.authTLS, tlsNow) })
			switch strings.ToUpper(fields[1]) {
			case "XOAUTH2":
				s.record(func() { s.xoauth2 = append(s.xoauth2, string(ir)) })
				if _, token, ok := ParseXOAuth2(ir); ok && token == s.opts.ValidToken {
					reply("235 2.7.0 Authentication successful")
					continue
				}
				reply("334 " + base64.StdEncoding.EncodeToString(ErrorChallenge))
				empty, ok := readLine()
				if !ok {
					return
				}
				s.record(func() { s.lines = append(s.lines, empty) })
				reply("535 5.7.3 Authentication unsuccessful")
			case "PLAIN":
				s.record(func() { s.plain = append(s.plain, string(ir)) })
				if string(ir) == "\x00"+s.opts.PlainUser+"\x00"+s.opts.PlainPassword {
					reply("235 2.7.0 Authentication successful")
				} else {
					reply("535 5.7.8 Authentication credentials invalid")
				}
			default:
				reply("504 unrecognized mechanism")
			}
		case "MAIL":
			s.record(func() { s.mailFrom = append(s.mailFrom, line) })
			reply("250 ok")
		case "RCPT":
			s.record(func() { s.rcptTo = append(s.rcptTo, line) })
			reply("250 ok")
		case "DATA":
			reply("354 go ahead")
			var buf bytes.Buffer
			for {
				l, ok := readLine()
				if !ok {
					return
				}
				if l == "." {
					break
				}
				buf.WriteString(l + "\r\n")
			}
			s.record(func() { s.data = append(s.data, buf.String()) })
			reply("250 queued")
		case "*":
			reply("501 auth aborted")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 unknown command")
		}
	}
}
