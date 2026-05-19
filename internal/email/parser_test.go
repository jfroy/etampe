package email

import (
	"encoding/base64"
	"slices"
	"testing"
)

func TestParseSimpleTextMessage(t *testing.T) {
	raw := []byte("From: App <app@example.com>\r\n" +
		"To: user@example.com\r\n" +
		"Subject: =?utf-8?q?Hello_=E2=9C=93?=\r\n" +
		"Traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01\r\n" +
		"X-App: homelab\r\n" +
		"\r\n" +
		"plain body\r\n")

	msg, err := Parse(raw, ParseOptions{
		EnvelopeFrom: "bounce@example.com",
		Recipients:   []string{"user@example.com", "hidden@example.com"},
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if msg.From.Address != "app@example.com" || msg.From.Name != "App" {
		t.Fatalf("unexpected sender: %#v", msg.From)
	}
	if msg.Subject != "Hello ✓" {
		t.Fatalf("unexpected subject: %q", msg.Subject)
	}
	if msg.Text != "plain body\r\n" {
		t.Fatalf("unexpected text body: %q", msg.Text)
	}
	if len(msg.To) != 1 || msg.To[0] != "user@example.com" {
		t.Fatalf("unexpected to recipients: %#v", msg.To)
	}
	if len(msg.BCC) != 1 || msg.BCC[0] != "hidden@example.com" {
		t.Fatalf("unexpected bcc recipients: %#v", msg.BCC)
	}
	if msg.Headers["X-App"] != "homelab" {
		t.Fatalf("expected X-App custom header, got %#v", msg.Headers)
	}

	parsed, err := ParseWithTrace(raw, ParseOptions{
		EnvelopeFrom: "bounce@example.com",
		Recipients:   []string{"user@example.com"},
	})
	if err != nil {
		t.Fatalf("ParseWithTrace returned error: %v", err)
	}
	if parsed.TraceHeader["traceparent"] == "" {
		t.Fatalf("expected traceparent, got %#v", parsed.TraceHeader)
	}
}

func TestParseMultipartMessageWithAttachment(t *testing.T) {
	attachment := base64.StdEncoding.EncodeToString([]byte("hello file"))
	raw := []byte("From: sender@example.com\r\n" +
		"To: user@example.com\r\n" +
		"Subject: multipart\r\n" +
		"Content-Type: multipart/mixed; boundary=mix\r\n" +
		"\r\n" +
		"--mix\r\n" +
		"Content-Type: multipart/alternative; boundary=alt\r\n" +
		"\r\n" +
		"--alt\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"plain\r\n" +
		"--alt\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>html</p>\r\n" +
		"--alt--\r\n" +
		"--mix\r\n" +
		"Content-Type: text/plain; name=note.txt\r\n" +
		"Content-Disposition: attachment; filename=note.txt\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		attachment + "\r\n" +
		"--mix--\r\n")

	msg, err := Parse(raw, ParseOptions{Recipients: []string{"user@example.com"}})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if msg.Text != "plain" {
		t.Fatalf("unexpected text body: %q", msg.Text)
	}
	if msg.HTML != "<p>html</p>" {
		t.Fatalf("unexpected HTML body: %q", msg.HTML)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %#v", msg.Attachments)
	}
	if msg.Attachments[0].Filename != "note.txt" {
		t.Fatalf("unexpected attachment filename: %#v", msg.Attachments[0])
	}
	if msg.Attachments[0].Content != attachment {
		t.Fatalf("unexpected attachment content: %#v", msg.Attachments[0])
	}
}

func TestParseUsesEnvelopeRecipientsAndIgnoresBccHeader(t *testing.T) {
	raw := []byte("From: sender@example.com\r\n" +
		"To: visible@example.com, ignored@example.com\r\n" +
		"Cc: copied@example.com\r\n" +
		"Bcc: leaked@example.com\r\n" +
		"Subject: recipients\r\n" +
		"\r\n" +
		"body\r\n")

	msg, err := Parse(raw, ParseOptions{
		Recipients: []string{"visible@example.com", "copied@example.com", "hidden@example.com"},
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !slices.Equal(msg.To, []string{"visible@example.com"}) {
		t.Fatalf("unexpected To: %#v", msg.To)
	}
	if !slices.Equal(msg.CC, []string{"copied@example.com"}) {
		t.Fatalf("unexpected Cc: %#v", msg.CC)
	}
	if !slices.Equal(msg.BCC, []string{"hidden@example.com"}) {
		t.Fatalf("unexpected Bcc: %#v", msg.BCC)
	}
}

func TestParseAllowsAttachmentOnlyMessage(t *testing.T) {
	raw := []byte("From: sender@example.com\r\n" +
		"To: user@example.com\r\n" +
		"Subject: attachment\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=file.bin\r\n" +
		"\r\n" +
		"abc")

	msg, err := Parse(raw, ParseOptions{Recipients: []string{"user@example.com"}})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if msg.Text != "" || msg.HTML != "" || len(msg.Attachments) != 1 {
		t.Fatalf("unexpected attachment-only message: %#v", msg)
	}
}

func TestParseRejectsTrulyEmptyMessage(t *testing.T) {
	raw := []byte("From: sender@example.com\r\n" +
		"To: user@example.com\r\n" +
		"Subject: empty\r\n" +
		"\r\n")

	if _, err := Parse(raw, ParseOptions{Recipients: []string{"user@example.com"}}); err == nil {
		t.Fatal("expected truly empty message to be rejected")
	}
}
