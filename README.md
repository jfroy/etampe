# 💌 Étampe

`etampe` is a small SMTP-to-Cloudflare Email Sending bridge for homelab Kubernetes clusters. It accepts SMTP on port `2525`, parses the message into Cloudflare's REST payload, sends through the Cloudflare Email Sending API, exposes `/healthz`, `/readyz`, and `/metrics` on port `8080`, and can export OpenTelemetry traces and metrics over OTLP.

## Configuration

Required:

- `CLOUDFLARE_ACCOUNT_ID`
- `CLOUDFLARE_API_TOKEN`

Common options:

- `CLOUDFLARE_FROM`: optional sender override for verified Cloudflare senders.
- `SMTP_USERNAME` and `SMTP_PASSWORD`: enable SMTP AUTH PLAIN when both are set.
- `SMTP_ALLOW_INSECURE_AUTH`: default `true` for private cluster networks; use TLS or NetworkPolicy when SMTP auth is enabled.
- `SMTP_ADDR`: default `:2525`.
- `HTTP_ADDR`: default `:8080`.
- `OTEL_EXPORTER_OTLP_ENDPOINT`: enables OTLP trace and metric export.

Notes:

- Cloudflare `429` and `5xx` responses are returned to SMTP clients as temporary `451` failures; sender queues should retry.
- Prometheus and OTLP metrics are both emitted. Avoid ingesting both into the same backend unless you de-duplicate.
- SMTP certificates are loaded at startup; restart the pod after Kubernetes secret rotation.
- SMTP `RCPT TO` is authoritative. `To:` and `Cc:` recipients not present in the envelope are stripped from the outbound Cloudflare payload, envelope recipients missing from visible headers are sent as `bcc`, and if no visible recipient remains the first envelope recipient is promoted to `to`.

## Run

```sh
docker run --rm -p 2525:2525 -p 8080:8080 \
  -e CLOUDFLARE_ACCOUNT_ID=... \
  -e CLOUDFLARE_API_TOKEN=... \
  ghcr.io/jfroy/etampe:latest
```

## Kubernetes

Create a secret:

```sh
kubectl create secret generic etampe \
  --from-literal=CLOUDFLARE_ACCOUNT_ID=... \
  --from-literal=CLOUDFLARE_API_TOKEN=...
```

Apply a private `ClusterIP` deployment:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: etampe
spec:
  selector:
    matchLabels:
      app: etampe
  template:
    metadata:
      labels:
        app: etampe
    spec:
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: etampe
          image: ghcr.io/jfroy/etampe:latest
          envFrom:
            - secretRef:
                name: etampe
          ports:
            - name: smtp
              containerPort: 2525
            - name: http
              containerPort: 8080
          readinessProbe:
            httpGet:
              path: /readyz
              port: http
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              memory: 128Mi
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            readOnlyRootFilesystem: true
---
apiVersion: v1
kind: Service
metadata:
  name: etampe
spec:
  selector:
    app: etampe
  ports:
    - name: smtp
      port: 2525
      targetPort: smtp
    - name: http
      port: 8080
      targetPort: http
```

Point in-cluster apps at `etampe.default.svc.cluster.local:2525`. Scrape `http://etampe:8080/metrics`.
