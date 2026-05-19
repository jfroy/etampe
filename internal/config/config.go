package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
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
	var validationErrs []error

	allowInsecureAuth, err := getBool("SMTP_ALLOW_INSECURE_AUTH", true)
	validationErrs = appendIfError(validationErrs, err)
	maxMessageBytes, err := getInt64("SMTP_MAX_MESSAGE_BYTES", defaultMaxMessageBytes)
	validationErrs = appendIfError(validationErrs, err)
	maxRecipients, err := getInt("SMTP_MAX_RECIPIENTS", 100)
	validationErrs = appendIfError(validationErrs, err)
	smtpReadTimeout, err := getDuration("SMTP_READ_TIMEOUT", 30*time.Second)
	validationErrs = appendIfError(validationErrs, err)
	smtpWriteTimeout, err := getDuration("SMTP_WRITE_TIMEOUT", 30*time.Second)
	validationErrs = appendIfError(validationErrs, err)
	smtpSendTimeout, err := getDuration("SMTP_SEND_TIMEOUT", 30*time.Second)
	validationErrs = appendIfError(validationErrs, err)
	cloudflareTimeout, err := getDuration("CLOUDFLARE_TIMEOUT", 15*time.Second)
	validationErrs = appendIfError(validationErrs, err)
	logLevel, err := getLogLevel("LOG_LEVEL", slog.LevelInfo)
	validationErrs = appendIfError(validationErrs, err)
	shutdownTimeout, err := getDuration("SHUTDOWN_TIMEOUT", 15*time.Second)
	validationErrs = appendIfError(validationErrs, err)

	cfg := Config{
		SMTP: SMTPConfig{
			Addr:              getEnv("SMTP_ADDR", ":2525"),
			Domain:            getEnv("SMTP_DOMAIN", "etampe.local"),
			Username:          os.Getenv("SMTP_USERNAME"),
			Password:          os.Getenv("SMTP_PASSWORD"),
			TLSCertFile:       os.Getenv("SMTP_TLS_CERT_FILE"),
			TLSKeyFile:        os.Getenv("SMTP_TLS_KEY_FILE"),
			AllowInsecureAuth: allowInsecureAuth,
			MaxMessageBytes:   maxMessageBytes,
			MaxRecipients:     maxRecipients,
			ReadTimeout:       smtpReadTimeout,
			WriteTimeout:      smtpWriteTimeout,
			SendTimeout:       smtpSendTimeout,
		},
		HTTP: HTTPConfig{
			Addr: getEnv("HTTP_ADDR", ":8080"),
		},
		Cloudflare: CloudflareConfig{
			AccountID: os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
			APIToken:  os.Getenv("CLOUDFLARE_API_TOKEN"),
			BaseURL:   getEnv("CLOUDFLARE_API_BASE_URL", "https://api.cloudflare.com/client/v4"),
			From:      os.Getenv("CLOUDFLARE_FROM"),
			Timeout:   cloudflareTimeout,
		},
		LogLevel:        logLevel,
		ServiceName:     getEnv("OTEL_SERVICE_NAME", "etampe"),
		ShutdownTimeout: shutdownTimeout,
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

func appendIfError(errs []error, err error) []error {
	if err == nil {
		return errs
	}
	return append(errs, err)
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback, fmt.Errorf("%s must be a Go duration like 30s or 5m: %w", key, err)
	}
	return parsed, nil
}

func getInt(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func getInt64(key string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func getBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}

func getLogLevel(key string, fallback slog.Level) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return fallback, fmt.Errorf("%s must be one of debug, info, warn, error", key)
	}
}
