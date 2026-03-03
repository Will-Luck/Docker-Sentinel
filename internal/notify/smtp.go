package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPSettings holds configuration for the SMTP email provider.
type SMTPSettings struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	From     string `json:"from"`
	To       string `json:"to"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	TLS      string `json:"tls,omitempty"`
}

// SMTP sends notifications via email.
type SMTP struct {
	host     string
	port     int
	from     string
	to       []string
	username string
	password string
	useTLS   bool
}

// NewSMTP constructs an SMTP notifier. tlsStr accepts "true", "1", or "yes" to
// enable implicit TLS (port 465 style); otherwise STARTTLS is attempted if advertised.
func NewSMTP(host string, port int, from, to, username, password, tlsStr string) *SMTP {
	var recipients []string
	for _, r := range strings.Split(to, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			recipients = append(recipients, r)
		}
	}
	useTLS := tlsStr == "true" || tlsStr == "1" || tlsStr == "yes"
	return &SMTP{
		host:     host,
		port:     port,
		from:     from,
		to:       recipients,
		username: username,
		password: password,
		useTLS:   useTLS,
	}
}

func (s *SMTP) Name() string { return "smtp" }

func (s *SMTP) Send(ctx context.Context, event Event) error {
	// Enforce a 30s overall timeout for the entire SMTP transaction.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	subject := formatTitle(event.Type)
	body := formatMessage(event)

	msg := "From: " + s.from + "\r\n" +
		"To: " + strings.Join(s.to, ", ") + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		body

	addr := net.JoinHostPort(s.host, fmt.Sprintf("%d", s.port))
	dialer := net.Dialer{Timeout: 10 * time.Second}

	var c *smtp.Client
	var err error

	if s.useTLS {
		tlsDialer := tls.Dialer{
			NetDialer: &dialer,
			Config:    &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12},
		}
		conn, dialErr := tlsDialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("smtp tls dial: %w", dialErr)
		}
		c, err = smtp.NewClient(conn, s.host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("smtp new client: %w", err)
		}
	} else {
		conn, dialErr := dialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("smtp dial: %w", dialErr)
		}
		c, err = smtp.NewClient(conn, s.host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("smtp new client: %w", err)
		}
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}); err != nil {
				c.Close()
				return fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}
	defer c.Close()

	// Check context before each step to respect the overall timeout.
	if ctx.Err() != nil {
		return fmt.Errorf("smtp timeout: %w", ctx.Err())
	}

	if s.username != "" {
		auth := smtp.PlainAuth("", s.username, s.password, s.host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if ctx.Err() != nil {
		return fmt.Errorf("smtp timeout: %w", ctx.Err())
	}

	if err := c.Mail(s.from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range s.to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt to: %w", err)
		}
	}

	if ctx.Err() != nil {
		return fmt.Errorf("smtp timeout: %w", ctx.Err())
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}

	return c.Quit()
}
