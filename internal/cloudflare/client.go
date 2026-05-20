package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jfroy/etampe/internal/email"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/jfroy/etampe/internal/cloudflare"

type Client struct {
	accountID string
	apiToken  string
	baseURL   string
	http      *http.Client
	logger    *slog.Logger
	tracer    trace.Tracer
}

type Config struct {
	AccountID  string
	APIToken   string
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
	Logger     *slog.Logger
}

func New(cfg Config) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.Timeout > 0 {
		// Preserve caller-provided transport and other settings while applying the service timeout.
		copied := *httpClient
		copied.Timeout = cfg.Timeout
		httpClient = &copied
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Client{
		accountID: cfg.AccountID,
		apiToken:  cfg.APIToken,
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		http:      httpClient,
		logger:    logger,
		tracer:    otel.Tracer(tracerName),
	}
}

type sendEmailRequest struct {
	From        email.Address      `json:"from"`
	To          []string           `json:"to"`
	CC          []string           `json:"cc,omitempty"`
	BCC         []string           `json:"bcc,omitempty"`
	ReplyTo     *email.Address     `json:"reply_to,omitempty"`
	Subject     string             `json:"subject"`
	Text        string             `json:"text,omitempty"`
	HTML        string             `json:"html,omitempty"`
	Headers     map[string]string  `json:"headers,omitempty"`
	Attachments []email.Attachment `json:"attachments,omitempty"`
}

type SendResult struct {
	Delivered        []string `json:"delivered,omitempty"`
	PermanentBounces []string `json:"permanent_bounces,omitempty"`
	Queued           []string `json:"queued,omitempty"`
}

type apiEnvelope[R any] struct {
	Success  bool         `json:"success"`
	Errors   []APIMessage `json:"errors"`
	Messages []APIMessage `json:"messages"`
	Result   R            `json:"result"`
}

type tokenVerifyResult struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ExpiresOn string `json:"expires_on,omitempty"`
	NotBefore string `json:"not_before,omitempty"`
}

type APIMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type APIError struct {
	StatusCode int
	Errors     []APIMessage
	Body       string
	ParseError error
}

func (e *APIError) Error() string {
	prefix := fmt.Sprintf("cloudflare email api returned HTTP %d", e.StatusCode)
	if e.ParseError != nil {
		if e.Body != "" {
			return fmt.Sprintf("%s with invalid JSON response: %v: %s", prefix, e.ParseError, e.Body)
		}
		return fmt.Sprintf("%s with invalid JSON response: %v", prefix, e.ParseError)
	}
	if len(e.Errors) > 0 {
		parts := make([]string, 0, len(e.Errors))
		for _, item := range e.Errors {
			if item.Code == 0 {
				parts = append(parts, item.Message)
				continue
			}
			parts = append(parts, fmt.Sprintf("%d: %s", item.Code, item.Message))
		}
		return fmt.Sprintf("%s: %s", prefix, strings.Join(parts, "; "))
	}
	if e.Body != "" {
		return fmt.Sprintf("%s: %s", prefix, e.Body)
	}
	return prefix
}

func (e *APIError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

func (e *APIError) Status() int {
	return e.StatusCode
}

func (e *APIError) Unwrap() error {
	return e.ParseError
}

func (c *Client) Send(ctx context.Context, msg email.Message) (SendResult, error) {
	ctx, span := c.tracer.Start(ctx, "cloudflare.email.send",
		trace.WithAttributes(
			attribute.Int("email.recipient_count", len(msg.To)+len(msg.CC)+len(msg.BCC)),
			attribute.Int("email.attachment_count", len(msg.Attachments)),
		),
	)
	defer span.End()

	if len(msg.To) == 0 {
		err := errors.New("at least one recipient is required")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return SendResult{}, err
	}
	if msg.From.Address == "" {
		err := errors.New("sender address is required")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return SendResult{}, err
	}
	if msg.Text == "" && msg.HTML == "" && len(msg.Attachments) == 0 {
		err := errors.New("message must include a text body, html body, or attachment")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return SendResult{}, err
	}

	payload := sendEmailRequest{
		From:        msg.From,
		To:          msg.To,
		CC:          msg.CC,
		BCC:         msg.BCC,
		ReplyTo:     msg.ReplyTo,
		Subject:     msg.Subject,
		Text:        msg.Text,
		HTML:        msg.HTML,
		Headers:     msg.Headers,
		Attachments: msg.Attachments,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return SendResult{}, fmt.Errorf("marshal cloudflare request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.sendURL(), bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return SendResult{}, fmt.Errorf("build cloudflare request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "etampe/1")

	res, err := c.http.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return SendResult{}, fmt.Errorf("send cloudflare request: %w", err)
	}
	defer func() {
		_ = res.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return SendResult{}, fmt.Errorf("read cloudflare response: %w", err)
	}

	var envelope apiEnvelope[SendResult]
	var parseErr error
	if len(raw) > 0 {
		parseErr = json.Unmarshal(raw, &envelope)
	}

	span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode))
	if parseErr != nil || res.StatusCode < 200 || res.StatusCode > 299 || !envelope.Success {
		apiErr := &APIError{
			StatusCode: res.StatusCode,
			Errors:     envelope.Errors,
			Body:       string(raw),
			ParseError: parseErr,
		}
		span.RecordError(apiErr)
		span.SetStatus(codes.Error, apiErr.Error())
		return SendResult{}, apiErr
	}

	if len(envelope.Messages) > 0 {
		c.logger.Debug("cloudflare email api messages", "messages", envelope.Messages)
	}
	span.SetStatus(codes.Ok, "")
	return envelope.Result, nil
}

// VerifyToken checks that the configured API token is valid and active.
func (c *Client) VerifyToken(ctx context.Context) error {
	ctx, span := c.tracer.Start(ctx, "cloudflare.token.verify")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/user/tokens/verify", nil)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("build token verify request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "etampe/1")

	res, err := c.http.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("send token verify request: %w", err)
	}
	defer func() {
		_ = res.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("read token verify response: %w", err)
	}

	var envelope apiEnvelope[tokenVerifyResult]
	var parseErr error
	if len(raw) > 0 {
		parseErr = json.Unmarshal(raw, &envelope)
	}

	span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode))
	if parseErr != nil || res.StatusCode < 200 || res.StatusCode > 299 || !envelope.Success {
		apiErr := &APIError{
			StatusCode: res.StatusCode,
			Errors:     envelope.Errors,
			Body:       string(raw),
			ParseError: parseErr,
		}
		span.RecordError(apiErr)
		span.SetStatus(codes.Error, apiErr.Error())
		return apiErr
	}

	if envelope.Result.Status != "active" {
		err := fmt.Errorf("cloudflare api token is %s", envelope.Result.Status)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	span.SetStatus(codes.Ok, "")
	c.logger.Info("cloudflare api token verified", "token_id", envelope.Result.ID, "status", envelope.Result.Status)
	return nil
}

func (c *Client) sendURL() string {
	accountID := url.PathEscape(c.accountID)
	return c.baseURL + "/accounts/" + accountID + "/email/sending/send"
}
