package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/jfroy/etampe/internal/cloudflare"
	"github.com/jfroy/etampe/internal/config"
	"github.com/jfroy/etampe/internal/email"
	"github.com/jfroy/etampe/internal/metrics"
	"github.com/jfroy/etampe/internal/observability"
	"github.com/jfroy/etampe/internal/smtpserver"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	logger.Info("starting etampe", "version", version, "commit", commit, "date", date)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	otelShutdown, err := observability.Init(ctx, cfg.ServiceName, version, logger)
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	defer func() {
		// Use a fresh context so signal cancellation does not prevent telemetry from flushing.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := otelShutdown.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown opentelemetry", "error", err)
		}
	}()

	registry := prometheus.NewRegistry()
	recorder, err := metrics.New(registry)
	if err != nil {
		return fmt.Errorf("initialize metrics: %w", err)
	}

	cloudflareClient := cloudflare.New(cloudflare.Config{
		AccountID: cfg.Cloudflare.AccountID,
		APIToken:  cfg.Cloudflare.APIToken,
		BaseURL:   cfg.Cloudflare.BaseURL,
		Timeout:   cfg.Cloudflare.Timeout,
		Logger:    logger,
		HTTPClient: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	})
	if err := cloudflareClient.VerifyToken(ctx); err != nil {
		return fmt.Errorf("cloudflare token verification failed: %w", err)
	}

	sender := smtpserver.SenderFunc(func(ctx context.Context, msg email.Message) error {
		_, err := cloudflareClient.Send(ctx, msg)
		return err
	})

	backend := smtpserver.NewBackend(smtpserver.Config{
		Username:        cfg.SMTP.Username,
		Password:        cfg.SMTP.Password,
		FromOverride:    cfg.Cloudflare.From,
		MaxMessageBytes: cfg.SMTP.MaxMessageBytes,
		SendTimeout:     cfg.SMTP.SendTimeout,
	}, sender, recorder, logger)

	smtpSrv, err := newSMTPServer(cfg, backend, logger)
	if err != nil {
		return err
	}
	httpSrv := newHTTPServer(cfg, registry)

	if cfg.SMTP.AuthEnabled() && cfg.SMTP.AllowInsecureAuth && cfg.SMTP.TLSCertFile == "" {
		logger.Warn("smtp auth is enabled without TLS; SMTP credentials may be sent in cleartext")
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("smtp server listening",
			"addr", cfg.SMTP.Addr,
			"domain", cfg.SMTP.Domain,
			"auth_enabled", cfg.SMTP.AuthEnabled(),
			"max_message_bytes", cfg.SMTP.MaxMessageBytes,
		)
		if err := smtpSrv.ListenAndServe(); err != nil && !errors.Is(err, smtp.ErrServerClosed) {
			reportServerError(errCh, logger, fmt.Errorf("smtp server: %w", err))
		}
	}()
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTP.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			reportServerError(errCh, logger, fmt.Errorf("http server: %w", err))
		}
	}()

	exitErr := waitForShutdown(ctx, errCh, logger)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := shutdownServers(shutdownCtx, httpSrv, smtpSrv); err != nil {
		return errors.Join(exitErr, err)
	}
	return exitErr
}

func newSMTPServer(cfg config.Config, backend *smtpserver.Backend, logger *slog.Logger) (*smtp.Server, error) {
	srv := smtp.NewServer(backend)
	srv.Network = "tcp"
	srv.Addr = cfg.SMTP.Addr
	srv.Domain = cfg.SMTP.Domain
	srv.MaxRecipients = cfg.SMTP.MaxRecipients
	srv.MaxMessageBytes = cfg.SMTP.MaxMessageBytes
	srv.ReadTimeout = cfg.SMTP.ReadTimeout
	srv.WriteTimeout = cfg.SMTP.WriteTimeout
	srv.AllowInsecureAuth = cfg.SMTP.AllowInsecureAuth
	srv.EnableSMTPUTF8 = true
	srv.ErrorLog = smtpserver.SlogSMTPLogger{Logger: logger}

	if cfg.SMTP.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.SMTP.TLSCertFile, cfg.SMTP.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load SMTP TLS certificate: %w", err)
		}
		srv.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
	}

	return srv, nil
}

func newHTTPServer(cfg config.Config, registry *prometheus.Registry) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{EnableOpenMetrics: true}))
	mux.HandleFunc("/healthz", okHandler)
	mux.HandleFunc("/readyz", okHandler)

	return &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func waitForShutdown(ctx context.Context, errCh <-chan error, logger *slog.Logger) error {
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		return nil
	case err := <-errCh:
		logger.Error("server failed", "error", err)
		return err
	}
}

func reportServerError(errCh chan<- error, logger *slog.Logger, err error) {
	select {
	case errCh <- err:
	default:
		logger.Error("server failed after shutdown started", "error", err)
	}
}

func shutdownServers(ctx context.Context, httpSrv *http.Server, smtpSrv *smtp.Server) error {
	errCh := make(chan error, 2)
	go func() {
		errCh <- httpSrv.Shutdown(ctx)
	}()
	go func() {
		errCh <- smtpSrv.Shutdown(ctx)
	}()
	return errors.Join(<-errCh, <-errCh)
}
