package smtpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jfroy/etampe/internal/email"
	"github.com/jfroy/etampe/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

func TestSessionRequiresAuthWhenConfigured(t *testing.T) {
	sess := newTestSession(t, Config{Username: "user", Password: "pass"}, SenderFunc(func(context.Context, email.Message) error {
		t.Fatal("sender should not be called")
		return nil
	}))

	if err := sess.Mail("sender@example.com", nil); !errors.Is(err, errAuthRequired) {
		t.Fatalf("expected auth required, got %v", err)
	}
	auth, err := sess.Auth("PLAIN")
	if err != nil {
		t.Fatalf("Auth returned error: %v", err)
	}
	_, done, err := auth.Next([]byte("\x00user\x00pass"))
	if err != nil || !done {
		t.Fatalf("auth failed: done=%v err=%v", done, err)
	}
	if err := sess.Mail("sender@example.com", nil); err != nil {
		t.Fatalf("Mail after auth returned error: %v", err)
	}
}

func TestSessionSendsParsedMessage(t *testing.T) {
	var got email.Message
	sess := newTestSession(t, Config{MaxMessageBytes: 1024}, SenderFunc(func(_ context.Context, msg email.Message) error {
		got = msg
		return nil
	}))

	if err := sess.Mail("bounce@example.com", nil); err != nil {
		t.Fatalf("Mail returned error: %v", err)
	}
	if err := sess.Rcpt("user@example.com", nil); err != nil {
		t.Fatalf("Rcpt returned error: %v", err)
	}
	raw := "From: sender@example.com\r\nTo: user@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
	if err := sess.Data(strings.NewReader(raw)); err != nil {
		t.Fatalf("Data returned error: %v", err)
	}
	if got.From.Address != "sender@example.com" || got.Text != "body\r\n" {
		t.Fatalf("unexpected message: %#v", got)
	}
	if sess.from != "" || len(sess.recipients) != 0 {
		t.Fatalf("expected session reset, got from=%q recipients=%#v", sess.from, sess.recipients)
	}
}

func TestSessionResetsAfterSendFailure(t *testing.T) {
	sess := newTestSession(t, Config{MaxMessageBytes: 1024}, SenderFunc(func(context.Context, email.Message) error {
		return errors.New("boom")
	}))

	if err := sess.Mail("bounce@example.com", nil); err != nil {
		t.Fatalf("Mail returned error: %v", err)
	}
	if err := sess.Rcpt("user@example.com", nil); err != nil {
		t.Fatalf("Rcpt returned error: %v", err)
	}
	raw := "From: sender@example.com\r\nTo: user@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
	if err := sess.Data(strings.NewReader(raw)); err == nil {
		t.Fatal("expected send failure")
	}
	if sess.from != "" || len(sess.recipients) != 0 {
		t.Fatalf("expected session reset, got from=%q recipients=%#v", sess.from, sess.recipients)
	}
}

func TestSessionResetsWhenDataHasNoRecipients(t *testing.T) {
	sess := newTestSession(t, Config{MaxMessageBytes: 1024}, SenderFunc(func(context.Context, email.Message) error {
		t.Fatal("sender should not be called")
		return nil
	}))

	if err := sess.Mail("bounce@example.com", nil); err != nil {
		t.Fatalf("Mail returned error: %v", err)
	}
	if err := sess.Data(strings.NewReader("From: sender@example.com\r\n\r\nbody\r\n")); err == nil {
		t.Fatal("expected no-recipient error")
	}
	if sess.from != "" || len(sess.recipients) != 0 {
		t.Fatalf("expected session reset, got from=%q recipients=%#v", sess.from, sess.recipients)
	}
}

func newTestSession(t *testing.T, cfg Config, sender Sender) *session {
	t.Helper()
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = 1024
	}
	recorder, err := metrics.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("metrics.New returned error: %v", err)
	}
	backend := NewBackend(cfg, sender, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	smtpSession, err := backend.NewSession(nil)
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = smtpSession.Logout()
	})
	sess, ok := smtpSession.(*session)
	if !ok {
		t.Fatalf("unexpected session type: %T", smtpSession)
	}
	return sess
}
