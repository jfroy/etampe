package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const defaultMaxMessageBytes = 5 * 1024 * 1024

type Config struct {
	SMTP            SMTPConfig
	HTTP            HTTPConfig
	Cloudflare      CloudflareConfig
	LogLevel        slog.Level
	ServiceName     string
	ShutdownTimeout time.Duration
}

type SMTPConfig struct {
	Addr              string
	Domain            string
	Username          string
	Password          string
	TLSCertFile       string
	TLSKeyFile        string
	AllowInsecureAuth bool
	MaxMessageBytes   int64
	MaxRecipients     int
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	SendTimeout       time.Duration
}

func (c SMTPConfig) AuthEnabled() bool {
	return c.Username != "" || c.Password != ""
}

type HTTPConfig struct {
	Addr string
}

type CloudflareConfig struct {
	AccountID string
	APIToken  string
	BaseURL   string
	From      string
	Timeout   time.Duration
}

func Load() (Config, error) {
	v := viper.New()

	// Map viper keys to env vars: dots become underscores, all uppercased.
	// e.g. "smtp.addr" -> "SMTP_ADDR", "cloudflare.api_token" -> "CLOUDFLARE_API_TOKEN"
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("smtp.addr", ":2525")
	v.SetDefault("smtp.domain", "etampe.local")
	v.SetDefault("smtp.allow_insecure_auth", true)
	v.SetDefault("smtp.max_message_bytes", defaultMaxMessageBytes)
	v.SetDefault("smtp.max_recipients", 100)
	v.SetDefault("smtp.read_timeout", "30s")
	v.SetDefault("smtp.write_timeout", "30s")
	v.SetDefault("smtp.send_timeout", "30s")
	v.SetDefault("http.addr", ":8080")
	v.SetDefault("cloudflare.api_base_url", "https://api.cloudflare.com/client/v4")
	v.SetDefault("cloudflare.timeout", "15s")
	v.SetDefault("log_level", "info")
	v.SetDefault("otel.service_name", "etampe")
	v.SetDefault("shutdown_timeout", "15s")

	// Optional config file (etampe.yaml / etampe.toml in . or /etc/etampe).
	v.SetConfigName("etampe")
	v.AddConfigPath(".")
	v.AddConfigPath("/etc/etampe")
	_ = v.ReadInConfig()

	var validationErrs []error

	logLevel, err := parseLogLevel(v.GetString("log_level"))
	if err != nil {
		validationErrs = append(validationErrs, err)
	}

	cfg := Config{
		SMTP: SMTPConfig{
			Addr:              v.GetString("smtp.addr"),
			Domain:            v.GetString("smtp.domain"),
			Username:          v.GetString("smtp.username"),
			Password:          v.GetString("smtp.password"),
			TLSCertFile:       v.GetString("smtp.tls_cert_file"),
			TLSKeyFile:        v.GetString("smtp.tls_key_file"),
			AllowInsecureAuth: v.GetBool("smtp.allow_insecure_auth"),
			MaxMessageBytes:   v.GetInt64("smtp.max_message_bytes"),
			MaxRecipients:     v.GetInt("smtp.max_recipients"),
			ReadTimeout:       v.GetDuration("smtp.read_timeout"),
			WriteTimeout:      v.GetDuration("smtp.write_timeout"),
			SendTimeout:       v.GetDuration("smtp.send_timeout"),
		},
		HTTP: HTTPConfig{
			Addr: v.GetString("http.addr"),
		},
		Cloudflare: CloudflareConfig{
			AccountID: v.GetString("cloudflare.account_id"),
			APIToken:  v.GetString("cloudflare.api_token"),
			BaseURL:   v.GetString("cloudflare.api_base_url"),
			From:      v.GetString("cloudflare.from"),
			Timeout:   v.GetDuration("cloudflare.timeout"),
		},
		LogLevel:        logLevel,
		ServiceName:     v.GetString("otel.service_name"),
		ShutdownTimeout: v.GetDuration("shutdown_timeout"),
	}

	if cfg.Cloudflare.AccountID == "" {
		validationErrs = append(validationErrs, errors.New("CLOUDFLARE_ACCOUNT_ID is required"))
	}
	if cfg.Cloudflare.APIToken == "" {
		validationErrs = append(validationErrs, errors.New("CLOUDFLARE_API_TOKEN is required"))
	}
	if cfg.SMTP.AuthEnabled() && (cfg.SMTP.Username == "" || cfg.SMTP.Password == "") {
		validationErrs = append(validationErrs, errors.New("set both SMTP_USERNAME and SMTP_PASSWORD, or neither"))
	}
	if (cfg.SMTP.TLSCertFile == "") != (cfg.SMTP.TLSKeyFile == "") {
		validationErrs = append(validationErrs, errors.New("set both SMTP_TLS_CERT_FILE and SMTP_TLS_KEY_FILE, or neither"))
	}
	if cfg.SMTP.MaxMessageBytes <= 0 {
		validationErrs = append(validationErrs, errors.New("SMTP_MAX_MESSAGE_BYTES must be positive"))
	}
	if cfg.SMTP.MaxRecipients <= 0 {
		validationErrs = append(validationErrs, errors.New("SMTP_MAX_RECIPIENTS must be positive"))
	}

	return cfg, errors.Join(validationErrs...)
}

func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("log_level must be one of debug, info, warn, error")
	}
}
