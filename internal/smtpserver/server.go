package smtpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/jfroy/etampe/internal/email"
	"github.com/jfroy/etampe/internal/metrics"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/jfroy/etampe/internal/smtpserver"

var errAuthRequired = &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Authentication required"}

type Sender interface {
	Send(context.Context, email.Message) error
}

type SenderFunc func(context.Context, email.Message) error

func (f SenderFunc) Send(ctx context.Context, msg email.Message) error {
	return f(ctx, msg)
}

type Config struct {
	Username        string
	Password        string
	FromOverride    string
	MaxMessageBytes int64
	SendTimeout     time.Duration
}

func (c Config) authEnabled() bool {
	return c.Username != "" || c.Password != ""
}

type Backend struct {
	cfg     Config
	sender  Sender
	metrics *metrics.Recorder
	logger  *slog.Logger
	tracer  trace.Tracer
}

func NewBackend(cfg Config, sender Sender, recorder *metrics.Recorder, logger *slog.Logger) *Backend {
	return &Backend{
		cfg:     cfg,
		sender:  sender,
		metrics: recorder,
		logger:  logger,
		tracer:  otel.Tracer(tracerName),
	}
}

func (b *Backend) NewSession(conn *smtp.Conn) (smtp.Session, error) {
	remoteAddr := ""
	if conn != nil && conn.Conn() != nil && conn.Conn().RemoteAddr() != nil {
		remoteAddr = conn.Conn().RemoteAddr().String()
	}
	b.metrics.SessionStarted(context.Background())
	b.logger.Debug("smtp session started", "remote_addr", remoteAddr)
	return &session{
		backend:    b,
		remoteAddr: remoteAddr,
	}, nil
}

type session struct {
	backend       *Backend
	remoteAddr    string
	authenticated bool
	from          string
	recipients    []string
}

func (s *session) AuthMechanisms() []string {
	if !s.backend.cfg.authEnabled() {
		return nil
	}
	return []string{"PLAIN"}
}

func (s *session) Auth(mech string) (sasl.Server, error) {
	if !s.backend.cfg.authEnabled() {
		return nil, smtp.ErrAuthUnsupported
	}
	if !strings.EqualFold(mech, "PLAIN") {
		return nil, smtp.ErrAuthUnknownMechanism
	}

	return sasl.NewPlainServer(func(identity, username, password string) error {
		if identity != "" && identity != username {
			return smtp.ErrAuthFailed
		}
		usernameOK := subtle.ConstantTimeCompare([]byte(username), []byte(s.backend.cfg.Username)) == 1
		passwordOK := subtle.ConstantTimeCompare([]byte(password), []byte(s.backend.cfg.Password)) == 1
		if !usernameOK || !passwordOK {
			return smtp.ErrAuthFailed
		}
		s.authenticated = true
		s.backend.logger.Debug("smtp authenticated", "remote_addr", s.remoteAddr, "username", username)
		return nil
	}), nil
}

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	if s.backend.cfg.authEnabled() && !s.authenticated {
		return errAuthRequired
	}
	s.from = from
	s.recipients = nil
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if s.backend.cfg.authEnabled() && !s.authenticated {
		return errAuthRequired
	}
	if strings.TrimSpace(to) == "" {
		return &smtp.SMTPError{Code: 553, EnhancedCode: smtp.EnhancedCode{5, 1, 3}, Message: "recipient address required"}
	}
	s.recipients = append(s.recipients, to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	if s.backend.cfg.authEnabled() && !s.authenticated {
		return errAuthRequired
	}
	defer s.Reset()

	if len(s.recipients) == 0 {
		return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 5, 1}, Message: "no recipients"}
	}

	raw, err := readLimited(r, s.backend.cfg.MaxMessageBytes)
	if err != nil {
		s.backend.metrics.MessageRejected(context.Background(), "too_large")
		return err
	}

	ctx := context.Background()
	parsed, err := email.ParseWithTrace(raw, email.ParseOptions{
		EnvelopeFrom: s.from,
		Recipients:   append([]string(nil), s.recipients...),
		FromOverride: s.backend.cfg.FromOverride,
	})
	if err != nil {
		s.backend.metrics.MessageRejected(ctx, "parse_error")
		s.backend.logger.Warn("smtp message rejected", "remote_addr", s.remoteAddr, "reason", "parse_error", "error", err)
		return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 6, 0}, Message: "invalid message"}
	}
	if carrier := parsed.TraceHeader; len(carrier) > 0 {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(carrier))
	}
	if s.backend.cfg.SendTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.backend.cfg.SendTimeout)
		defer cancel()
	}

	ctx, span := s.backend.tracer.Start(ctx, "smtp.message",
		trace.WithAttributes(
			attribute.String("smtp.remote_addr", s.remoteAddr),
			attribute.Int("smtp.recipient_count", len(s.recipients)),
			attribute.Int("email.message.size", len(raw)),
		),
	)
	defer span.End()

	msg := parsed.Message

	start := time.Now()
	err = s.backend.sender.Send(ctx, msg)
	duration := time.Since(start)
	if err != nil {
		statusCode := statusCode(err)
		result := "error"
		if isTemporary(err) {
			result = "temporary_error"
		}
		s.backend.metrics.CloudflareRequest(ctx, result, statusCode, duration)
		s.backend.metrics.MessageRejected(ctx, result)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		s.backend.logger.Error("cloudflare send failed",
			"remote_addr", s.remoteAddr,
			"recipient_count", len(s.recipients),
			"status_code", statusCode,
			"error", err,
		)
		return smtpErrorFor(err)
	}

	s.backend.metrics.CloudflareRequest(ctx, "success", 200, duration)
	s.backend.metrics.MessageAccepted(ctx, msg.RawSize, len(s.recipients))
	span.SetAttributes(attribute.String("email.sender", msg.From.Address))
	span.SetStatus(codes.Ok, "")
	s.backend.logger.Info("smtp message sent",
		"remote_addr", s.remoteAddr,
		"recipient_count", len(s.recipients),
		"attachment_count", len(msg.Attachments),
		"message_size_bytes", msg.RawSize,
	)
	return nil
}

func (s *session) Reset() {
	s.from = ""
	s.recipients = nil
}

func (s *session) Logout() error {
	s.backend.metrics.SessionEnded(context.Background())
	s.backend.logger.Debug("smtp session ended", "remote_addr", s.remoteAddr)
	return nil
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read message: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, smtp.ErrDataTooLarge
	}
	return raw, nil
}

type temporary interface {
	Temporary() bool
}

type statuser interface {
	Status() int
}

type timeout interface {
	Timeout() bool
}

func isTemporary(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var temporaryErr temporary
	if errors.As(err, &temporaryErr) && temporaryErr.Temporary() {
		return true
	}
	var timeoutErr timeout
	return errors.As(err, &timeoutErr) && timeoutErr.Timeout()
}

func statusCode(err error) int {
	var statusErr statuser
	if errors.As(err, &statusErr) {
		return statusErr.Status()
	}
	return 0
}

func smtpErrorFor(err error) error {
	if isTemporary(err) {
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "upstream mail service temporarily unavailable"}
	}
	return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 3, 0}, Message: "upstream mail service rejected message"}
}

type SlogSMTPLogger struct {
	Logger *slog.Logger
}

func (l SlogSMTPLogger) Printf(format string, args ...interface{}) {
	l.Logger.Error("smtp server error", "message", fmt.Sprintf(format, args...))
}

func (l SlogSMTPLogger) Println(args ...interface{}) {
	l.Logger.Error("smtp server error", "message", strings.TrimSpace(fmt.Sprintln(args...)))
}
