package config

import (
	"log/slog"
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
	// caarlos0/env includes the Go field name in parse errors, e.g.:
	//   env: parse error on field "MaxMessageBytes" of type "int64": ...
	message := err.Error()
	for _, want := range []string{
		"MaxMessageBytes",
		"ReadTimeout",
		"AllowInsecureAuth",
		"LogLevel",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected %q in error: %s", want, message)
		}
	}
}

func TestLoadRejectsMissingRequiredFields(t *testing.T) {
	_, err := Load()
	if err == nil {
		t.Fatal("expected validation error for missing required fields")
	}
	message := err.Error()
	for _, want := range []string{"CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_API_TOKEN"} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected %q in error: %s", want, message)
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
	t.Setenv("LOG_LEVEL", "DEBUG")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.SMTP.MaxMessageBytes != 1024 {
		t.Errorf("MaxMessageBytes = %d, want 1024", cfg.SMTP.MaxMessageBytes)
	}
	if cfg.SMTP.MaxRecipients != 10 {
		t.Errorf("MaxRecipients = %d, want 10", cfg.SMTP.MaxRecipients)
	}
	if cfg.SMTP.AllowInsecureAuth {
		t.Error("AllowInsecureAuth should be false")
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want DEBUG", cfg.LogLevel)
	}
}

func TestLoadRejectsMismatchedSMTPAuth(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "token")
	t.Setenv("SMTP_USERNAME", "user")
	// SMTP_PASSWORD intentionally omitted

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for mismatched SMTP auth")
	}
	if !strings.Contains(err.Error(), "SMTP_USERNAME") && !strings.Contains(err.Error(), "SMTP_PASSWORD") {
		t.Errorf("expected SMTP auth error, got: %s", err)
	}
}

func TestLoadRejectsMismatchedTLS(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "token")
	t.Setenv("SMTP_TLS_CERT_FILE", "/path/to/cert.pem")
	// SMTP_TLS_KEY_FILE intentionally omitted

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for mismatched TLS config")
	}
	if !strings.Contains(err.Error(), "SMTP_TLS_CERT_FILE") && !strings.Contains(err.Error(), "SMTP_TLS_KEY_FILE") {
		t.Errorf("expected TLS pair error, got: %s", err)
	}
}
