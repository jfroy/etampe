package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jfroy/etampe/internal/email"
)

func TestSendSuccess(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/account/email/sending/send" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected authorization header: %s", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"delivered":["user@example.com"]}}`))
	}))
	defer server.Close()

	client := New(Config{AccountID: "account", APIToken: "token", BaseURL: server.URL})
	result, err := client.Send(context.Background(), email.Message{
		From:    email.Address{Address: "sender@example.com"},
		To:      []string{"user@example.com"},
		Subject: "hello",
		Text:    "body",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if len(result.Delivered) != 1 || result.Delivered[0] != "user@example.com" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got["text"] != "body" {
		t.Fatalf("unexpected payload: %#v", got)
	}
}

func TestSendLogsSuccessMessagesAtDebug(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"messages":[{"code":2000,"message":"queued"}],"result":{"queued":["user@example.com"]}}`))
	}))
	defer server.Close()

	client := New(Config{
		AccountID: "account",
		APIToken:  "token",
		BaseURL:   server.URL,
		Logger:    logger,
	})
	if _, err := client.Send(context.Background(), validMessage()); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if got := logs.String(); !strings.Contains(got, "cloudflare email api messages") || !strings.Contains(got, "queued") {
		t.Fatalf("expected Cloudflare messages to be logged, got %q", got)
	}
}

func TestSendRejectsTrulyEmptyMessage(t *testing.T) {
	client := New(Config{AccountID: "account", APIToken: "token", BaseURL: "http://127.0.0.1"})
	_, err := client.Send(context.Background(), email.Message{
		From: email.Address{Address: "sender@example.com"},
		To:   []string{"user@example.com"},
	})
	if err == nil || !strings.Contains(err.Error(), "body, html body, or attachment") {
		t.Fatalf("expected content validation error, got %v", err)
	}
}

func TestSendAllowsAttachmentOnlyMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := got["text"]; ok {
			t.Fatalf("unexpected text field in payload: %#v", got)
		}
		if _, ok := got["html"]; ok {
			t.Fatalf("unexpected html field in payload: %#v", got)
		}
		attachments, ok := got["attachments"].([]any)
		if !ok || len(attachments) != 1 {
			t.Fatalf("expected one attachment, got %#v", got["attachments"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"queued":["user@example.com"]}}`))
	}))
	defer server.Close()

	client := New(Config{AccountID: "account", APIToken: "token", BaseURL: server.URL})
	_, err := client.Send(context.Background(), email.Message{
		From: email.Address{Address: "sender@example.com"},
		To:   []string{"user@example.com"},
		Attachments: []email.Attachment{{
			Content:     "YWJj",
			Filename:    "file.txt",
			Type:        "text/plain",
			Disposition: "attachment",
		}},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
}

func TestSendSurfacesMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer server.Close()

	client := New(Config{AccountID: "account", APIToken: "token", BaseURL: server.URL})
	_, err := client.Send(context.Background(), validMessage())
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusOK || apiErr.ParseError == nil {
		t.Fatalf("unexpected APIError: %#v", apiErr)
	}
}

func TestSendClassifiesCloudflareErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		temporary bool
	}{
		{name: "bad request", status: http.StatusBadRequest},
		{name: "rate limited", status: http.StatusTooManyRequests, temporary: true},
		{name: "server error", status: http.StatusBadGateway, temporary: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"failed"}]}`))
			}))
			defer server.Close()

			client := New(Config{AccountID: "account", APIToken: "token", BaseURL: server.URL})
			_, err := client.Send(context.Background(), validMessage())
			if err == nil {
				t.Fatal("expected error")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected APIError, got %T", err)
			}
			if apiErr.StatusCode != tc.status {
				t.Fatalf("unexpected status: %d", apiErr.StatusCode)
			}
			if apiErr.Temporary() != tc.temporary {
				t.Fatalf("unexpected temporary classification: %v", apiErr.Temporary())
			}
			if len(apiErr.Errors) != 1 || apiErr.Errors[0].Code != 1000 {
				t.Fatalf("unexpected API errors: %#v", apiErr.Errors)
			}
		})
	}
}

func validMessage() email.Message {
	return email.Message{
		From:    email.Address{Address: "sender@example.com"},
		To:      []string{"user@example.com"},
		Subject: "hello",
		Text:    "body",
	}
}
