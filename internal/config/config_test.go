package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsInvalidTypedEnv(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "token")
	t.Setenv("SMTP_MAX_MESSAGE_BYTES", "5MB")
	t.Setenv("SMTP_READ_TIMEOUT", "30")
	t.Setenv("SMTP_ALLOW_INSECURE_AUTH", "maybe")
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := Load()
	if err == nil {
		t.Fatal("expected validation error")
	}
	message := err.Error()
	for _, want := range []string{
		"SMTP_MAX_MESSAGE_BYTES must be an integer",
		"SMTP_READ_TIMEOUT must be a Go duration",
		"SMTP_ALLOW_INSECURE_AUTH must be a boolean",
		"LOG_LEVEL must be one of",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected %q in %q", want, message)
		}
	}
}

func TestLoadAcceptsValidConfig(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "token")
	t.Setenv("SMTP_MAX_MESSAGE_BYTES", "1024")
	t.Setenv("SMTP_MAX_RECIPIENTS", "10")
	t.Setenv("SMTP_READ_TIMEOUT", "10s")
	t.Setenv("SMTP_WRITE_TIMEOUT", "11s")
	t.Setenv("SMTP_SEND_TIMEOUT", "12s")
	t.Setenv("CLOUDFLARE_TIMEOUT", "13s")
	t.Setenv("SHUTDOWN_TIMEOUT", "14s")
	t.Setenv("SMTP_ALLOW_INSECURE_AUTH", "false")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.SMTP.MaxMessageBytes != 1024 || cfg.SMTP.MaxRecipients != 10 {
		t.Fatalf("unexpected SMTP limits: %#v", cfg.SMTP)
	}
	if cfg.SMTP.AllowInsecureAuth {
		t.Fatal("expected insecure auth to be disabled")
	}
}
