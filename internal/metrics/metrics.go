package metrics

import (
	"context"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/jfroy/etampe"

type Recorder struct {
	smtpActiveProm        prometheus.Gauge
	smtpMessagesProm      *prometheus.CounterVec
	messageSizeProm       prometheus.Histogram
	recipientsProm        prometheus.Histogram
	cloudflareReqProm     *prometheus.CounterVec
	cloudflareLatencyProm *prometheus.HistogramVec

	smtpActiveOTel        otelmetric.Int64UpDownCounter
	smtpMessagesOTel      otelmetric.Int64Counter
	messageSizeOTel       otelmetric.Int64Histogram
	recipientsOTel        otelmetric.Int64Histogram
	cloudflareReqOTel     otelmetric.Int64Counter
	cloudflareLatencyOTel otelmetric.Float64Histogram
}

func New(registry *prometheus.Registry) (*Recorder, error) {
	meter := otel.Meter(meterName)

	recorder := &Recorder{
		smtpActiveProm: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "etampe",
			Subsystem: "smtp",
			Name:      "active_sessions",
			Help:      "Current number of active SMTP sessions.",
		}),
		smtpMessagesProm: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "etampe",
			Subsystem: "smtp",
			Name:      "messages_total",
			Help:      "SMTP messages handled by result and reason.",
		}, []string{"result", "reason"}),
		messageSizeProm: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "etampe",
			Subsystem: "smtp",
			Name:      "message_size_bytes",
			Help:      "Accepted SMTP message size in bytes.",
			Buckets:   []float64{1024, 10 * 1024, 100 * 1024, 500 * 1024, 1024 * 1024, 2.5 * 1024 * 1024, 5 * 1024 * 1024},
		}),
		recipientsProm: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "etampe",
			Subsystem: "smtp",
			Name:      "recipients",
			Help:      "Accepted SMTP recipients per message.",
			Buckets:   []float64{1, 2, 5, 10, 25, 50, 100},
		}),
		cloudflareReqProm: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "etampe",
			Subsystem: "cloudflare",
			Name:      "requests_total",
			Help:      "Cloudflare Email Sending API requests by result and HTTP status.",
		}, []string{"result", "status_code"}),
		cloudflareLatencyProm: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "etampe",
			Subsystem: "cloudflare",
			Name:      "request_duration_seconds",
			Help:      "Cloudflare Email Sending API request latency.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"result", "status_code"}),
	}

	toRegister := []prometheus.Collector{
		recorder.smtpActiveProm,
		recorder.smtpMessagesProm,
		recorder.messageSizeProm,
		recorder.recipientsProm,
		recorder.cloudflareReqProm,
		recorder.cloudflareLatencyProm,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	}
	for _, collector := range toRegister {
		if err := registry.Register(collector); err != nil {
			return nil, err
		}
	}

	var err error
	if recorder.smtpActiveOTel, err = meter.Int64UpDownCounter("etampe.smtp.active_sessions"); err != nil {
		return nil, err
	}
	if recorder.smtpMessagesOTel, err = meter.Int64Counter("etampe.smtp.messages"); err != nil {
		return nil, err
	}
	if recorder.messageSizeOTel, err = meter.Int64Histogram("etampe.smtp.message_size_bytes"); err != nil {
		return nil, err
	}
	if recorder.recipientsOTel, err = meter.Int64Histogram("etampe.smtp.recipients"); err != nil {
		return nil, err
	}
	if recorder.cloudflareReqOTel, err = meter.Int64Counter("etampe.cloudflare.requests"); err != nil {
		return nil, err
	}
	if recorder.cloudflareLatencyOTel, err = meter.Float64Histogram("etampe.cloudflare.request_duration_seconds"); err != nil {
		return nil, err
	}

	return recorder, nil
}

func (r *Recorder) SessionStarted(ctx context.Context) {
	r.smtpActiveProm.Inc()
	r.smtpActiveOTel.Add(ctx, 1)
}

func (r *Recorder) SessionEnded(ctx context.Context) {
	r.smtpActiveProm.Dec()
	r.smtpActiveOTel.Add(ctx, -1)
}

func (r *Recorder) MessageAccepted(ctx context.Context, sizeBytes int, recipientCount int) {
	r.smtpMessagesProm.WithLabelValues("accepted", "sent").Inc()
	r.messageSizeProm.Observe(float64(sizeBytes))
	r.recipientsProm.Observe(float64(recipientCount))

	r.smtpMessagesOTel.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("result", "accepted"),
		attribute.String("reason", "sent"),
	))
	r.messageSizeOTel.Record(ctx, int64(sizeBytes))
	r.recipientsOTel.Record(ctx, int64(recipientCount))
}

func (r *Recorder) MessageRejected(ctx context.Context, reason string) {
	r.smtpMessagesProm.WithLabelValues("rejected", reason).Inc()
	r.smtpMessagesOTel.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("result", "rejected"),
		attribute.String("reason", reason),
	))
}

func (r *Recorder) CloudflareRequest(ctx context.Context, result string, statusCode int, duration time.Duration) {
	status := "none"
	if statusCode > 0 {
		status = strconv.Itoa(statusCode)
	}
	r.cloudflareReqProm.WithLabelValues(result, status).Inc()
	r.cloudflareLatencyProm.WithLabelValues(result, status).Observe(duration.Seconds())
	r.cloudflareReqOTel.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("result", result),
		attribute.String("status_code", status),
	))
	r.cloudflareLatencyOTel.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("result", result),
		attribute.String("status_code", status),
	))
}
