package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/migalsp/costdeck-operator/internal/netguard"
)

// Mail is one email and the server to send it through.
type Mail struct {
	Host     string
	Port     int
	Security string // starttls (default), tls or none
	Username string
	Password string
	From     string
	To       []string
	Subject  string
	Body     string
}

// Port is the server port, defaulting by Security.
func (m Mail) port() int {
	if m.Port > 0 {
		return m.Port
	}
	if m.Security == "tls" {
		return 465
	}
	return 587
}

// SendMail delivers m. Without TLS no password is sent: a relay inside the cluster takes
// mail without signing in.
func SendMail(ctx context.Context, m Mail) error {
	if m.Host == "" || m.From == "" || len(m.To) == 0 {
		return errors.New("email needs a server, a sender and at least one recipient")
	}
	if m.Username != "" && m.Security == "none" {
		return errors.New("refusing to send the SMTP password without TLS: use starttls or tls")
	}
	from, to, err := addresses(m.From, m.To)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := netguard.Dialer(10*time.Second).DialContext(ctx, "tcp", net.JoinHostPort(m.Host, strconv.Itoa(m.port())))
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	tlsConfig := &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}
	if m.Security == "tls" {
		conn = tls.Client(conn, tlsConfig)
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if m.Security == "" || m.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the SMTP server does not offer STARTTLS; choose tls or none")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if m.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.Username, m.Password, m.Host)); err != nil {
			return fmt.Errorf("sign in: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	for _, a := range to {
		if err := c.Rcpt(a.Address); err != nil {
			return fmt.Errorf("recipient %s: %w", a.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(message(m, from, to, time.Now())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// addresses parses the sender and the recipients. Headers and SMTP commands are written
// from what was parsed, so an address with a line break in it cannot add headers (a Bcc)
// of its own: the test from the settings sends a form that was never saved.
func addresses(from string, to []string) (*mail.Address, []*mail.Address, error) {
	f, err := mail.ParseAddress(from)
	if err != nil {
		return nil, nil, fmt.Errorf("sender %q: %w", from, err)
	}
	out := make([]*mail.Address, 0, len(to))
	for _, t := range to {
		a, err := mail.ParseAddress(t)
		if err != nil {
			return nil, nil, fmt.Errorf("recipient %q: %w", t, err)
		}
		out = append(out, a)
	}
	return f, out, nil
}

// message renders m as a MIME message with a quoted-printable plain-text body.
func message(m Mail, from *mail.Address, to []*mail.Address, now time.Time) []byte {
	var b bytes.Buffer
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := "costdeck"
	if _, at, ok := strings.Cut(from.Address, "@"); ok {
		domain = at
	}
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", from.String())
	recipients := make([]string, len(to))
	for i, a := range to {
		recipients[i] = a.String()
	}
	header("To", strings.Join(recipients, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", `text/plain; charset="utf-8"`)
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	_, _ = qp.Write([]byte(strings.ReplaceAll(m.Body, "\n", "\r\n")))
	_ = qp.Close()
	return b.Bytes()
}

// CheckAddresses validates a sender and recipients before they are stored.
func CheckAddresses(from string, to []string) error {
	_, _, err := addresses(from, to)
	return err
}
