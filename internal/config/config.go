package config

import (
	"errors"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	SMTP            SMTPConfig
	HTTP            HTTPConfig
	Cloudflare      CloudflareConfig
	LogLevel        slog.Level    `env:"LOG_LEVEL"         envDefault:"INFO"`
	ServiceName     string        `env:"OTEL_SERVICE_NAME" envDefault:"etampe"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT"  envDefault:"15s"`
}

type SMTPConfig struct {
	Addr              string        `env:"SMTP_ADDR"                envDefault:":2525"`
	Domain            string        `env:"SMTP_DOMAIN"              envDefault:"etampe.local"`
	Username          string        `env:"SMTP_USERNAME"`
	Password          string        `env:"SMTP_PASSWORD"`
	TLSCertFile       string        `env:"SMTP_TLS_CERT_FILE"`
	TLSKeyFile        string        `env:"SMTP_TLS_KEY_FILE"`
	AllowInsecureAuth bool          `env:"SMTP_ALLOW_INSECURE_AUTH" envDefault:"true"`
	MaxMessageBytes   int64         `env:"SMTP_MAX_MESSAGE_BYTES"   envDefault:"5242880"`
	MaxRecipients     int           `env:"SMTP_MAX_RECIPIENTS"      envDefault:"100"`
	ReadTimeout       time.Duration `env:"SMTP_READ_TIMEOUT"        envDefault:"30s"`
	WriteTimeout      time.Duration `env:"SMTP_WRITE_TIMEOUT"       envDefault:"30s"`
	SendTimeout       time.Duration `env:"SMTP_SEND_TIMEOUT"        envDefault:"30s"`
}

func (c SMTPConfig) AuthEnabled() bool {
	return c.Username != "" || c.Password != ""
}

type HTTPConfig struct {
	Addr string `env:"HTTP_ADDR" envDefault:":8080"`
}

type CloudflareConfig struct {
	AccountID string        `env:"CLOUDFLARE_ACCOUNT_ID,required"`
	APIToken  string        `env:"CLOUDFLARE_API_TOKEN,required"`
	BaseURL   string        `env:"CLOUDFLARE_API_BASE_URL" envDefault:"https://api.cloudflare.com/client/v4"`
	From      string        `env:"CLOUDFLARE_FROM"`
	Timeout   time.Duration `env:"CLOUDFLARE_TIMEOUT" envDefault:"15s"`
}

func Load() (Config, error) {
	cfg, parseErr := env.ParseAs[Config]()

	var validationErrs []error
	if parseErr != nil {
		validationErrs = append(validationErrs, parseErr)
	}

	if (cfg.SMTP.Username == "") != (cfg.SMTP.Password == "") {
		validationErrs = append(validationErrs, errors.New("set both SMTP_USERNAME and SMTP_PASSWORD, or neither"))
	}
	if (cfg.SMTP.TLSCertFile == "") != (cfg.SMTP.TLSKeyFile == "") {
		validationErrs = append(validationErrs, errors.New("set both SMTP_TLS_CERT_FILE and SMTP_TLS_KEY_FILE, or neither"))
	}
	// Guard range checks: a parse failure for these fields already produces an
	// error and leaves the field at zero, so avoid a redundant second error.
	if parseErr == nil {
		if cfg.SMTP.MaxMessageBytes <= 0 {
			validationErrs = append(validationErrs, errors.New("SMTP_MAX_MESSAGE_BYTES must be positive"))
		}
		if cfg.SMTP.MaxRecipients <= 0 {
			validationErrs = append(validationErrs, errors.New("SMTP_MAX_RECIPIENTS must be positive"))
		}
	}

	return cfg, errors.Join(validationErrs...)
}
