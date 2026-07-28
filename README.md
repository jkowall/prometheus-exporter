# Spacelift Prometheus Exporter

This repository contains a Prometheus exporter for exporting metrics from your Spacelift account.

![Dashboard Example](dashboard-example.png)

## Quick Start

The Spacelift exporter is provided as a statically linked Go binary and a Docker container. You can
find the latest release [here](https://github.com/spacelift-io/prometheus-exporter/releases/latest).
The Docker container is available from our public container registry:
`public.ecr.aws/spacelift/promex`.

### Authentication

The exporter uses
[Spacelift API keys](https://docs.spacelift.io/integrations/api#spacelift-api-key-greater-than-token)
to authenticate, and also needs to know your Spacelift account API endpoint. Your API endpoint is in
the format `https://<account>.app.spacelift.io`, for example `https://my-account.app.spacelift.io`.

**NOTE:** the API key you use must be an Admin key because some of the API fields used for the metrics
require administrative access.

#### OIDC API keys with rotating secrets

If your API key is configured for OIDC (the key ID has the form `oidc::<issuer>::<key-id>`), the
"secret" is an OIDC JWT issued by your identity provider rather than a static string. Use
`--api-key-secret-file` (or `SPACELIFT_PROMEX_API_KEY_SECRET_FILE`) to point at the file that holds
the token. The file is re-read on every token refresh, so projected Kubernetes service-account
tokens that rotate on disk are picked up automatically without restarting the exporter.

### Running via the Binary

Download the exporter binary from our
[releases](https://github.com/spacelift-io/prometheus-exporter/releases/latest) page, make sure it's
added to your PATH, and then use the `spacelift-promex serve` command to run the exporter binary:

```shell
spacelift-promex serve --api-endpoint "https://<account>.app.spacelift.io" --api-key-id "<API Key ID>" --api-key-secret "<API Key Secret>"
```

### Running via Docker

Use the following command to run the exporter via Docker:

```shell
docker run -it --rm -p 9953:9953 -e "SPACELIFT_PROMEX_API_ENDPOINT=https://<account>.app.spacelift.io" \
  -e "SPACELIFT_PROMEX_API_KEY_ID=<API Key ID>" \
  -e "SPACELIFT_PROMEX_API_KEY_SECRET=<API Key Secret>" \
  public.ecr.aws/spacelift/promex
```

### Running in Kubernetes

You can use the following Deployment definition to run the exporter:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: spacelift-promex
  labels:
    app: spacelift-promex
spec:
  replicas: 1
  selector:
    matchLabels:
      app: spacelift-promex
  template:
    metadata:
      labels:
        app: spacelift-promex
    spec:
      containers:
        - name: spacelift-promex
          image: public.ecr.aws/spacelift/promex:latest
          ports:
            - name: metrics
              containerPort: 9953
          readinessProbe:
            httpGet:
              path: /health
              port: metrics
            periodSeconds: 5
          env:
            - name: "SPACELIFT_PROMEX_API_ENDPOINT"
              value: "" # Add your endpoint here
            - name: "SPACELIFT_PROMEX_API_KEY_ID"
              value: "" # Add your API key here
            - name: "SPACELIFT_PROMEX_API_KEY_SECRET"
              value: "" # Add your secret here
            - name: "SPACELIFT_PROMEX_LISTEN_ADDRESS"
              value: ":9953"
```

To use the example deployment, make sure you fill in the API endpoint, API Key ID and API Key
Secret, as explained in the comments. For a production deployment we would recommend making use of
Kubernetes secrets rather than embedding the API key values directly.

## Port Number

By default the exporter listens on port 9953. To change this use the `--listen-address` flag or the
`SPACELIFT_PROMEX_LISTEN_ADDRESS` environment variable:

```shell
spacelift-promex serve --listen-address ":9999" --api-endpoint "https://<account>.app.spacelift.io" --api-key-id "<API Key ID>" --api-key-secret "<API Key Secret>"
```

## Custom CA Certificate

If your Spacelift endpoint uses a certificate chain that is not trusted by system CAs, you can add
an extra trusted root certificate with `--ca-cert-path` or `SPACELIFT_PROMEX_CA_CERT_PATH`.
The exporter keeps system CAs and appends the certificate from the path you provide.

```shell
spacelift-promex serve --ca-cert-path "/certs/spacelift-ca.crt" --api-endpoint "https://<account>.app.spacelift.io" --api-key-id "<API Key ID>" --api-key-secret "<API Key Secret>"
```

## Help

To get information about all the available commands and options, use the `help` command:

```shell
$ spacelift-promex help
NAME:
   spacelift-promex - Exports metrics from your Spacelift account to Prometheus

USAGE:
   spacelift-promex [global options] command [command options] [arguments...]

VERSION:
   0.0.1

COMMANDS:
   serve    Starts the Prometheus exporter
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --help, -h     show help (default: false)
   --version, -v  print the version (default: false)


COPYRIGHT:
   Copyright (c) 2022 spacelift-io
```

To get information about an individual command, use the `--help` flag:

```shell
$ spacelift-promex serve --help
NAME:
   spacelift-promex serve - Starts the Prometheus exporter

USAGE:
   spacelift-promex serve [command options] [arguments...]

OPTIONS:
   --api-endpoint value, -e value    Your spacelift API endpoint (e.g. https://myaccount.app.spacelift.io) [$SPACELIFT_PROMEX_API_ENDPOINT]
   --ca-cert-path value              Path to a PEM-encoded CA certificate to trust in addition to system certificates [$SPACELIFT_PROMEX_CA_CERT_PATH]
   --api-key-id value, -k value      Your spacelift API key ID [$SPACELIFT_PROMEX_API_KEY_ID]
   --api-key-secret value, -s value  Your spacelift API key secret. Mutually exclusive with --api-key-secret-file. [$SPACELIFT_PROMEX_API_KEY_SECRET]
   --api-key-secret-file value       Path to a file containing the spacelift API key secret. The file is re-read on every token refresh, so this is the right choice for rotating secrets such as Kubernetes projected service-account tokens used with OIDC API keys. Mutually exclusive with --api-key-secret. [$SPACELIFT_PROMEX_API_KEY_SECRET_FILE]
   --is-development, -d              Uses settings appropriate during local development (default: false) [$SPACELIFT_PROMEX_IS_DEVELOPMENT]
   --listen-address value, -l value  The address to listen on for HTTP requests (default: ":9953") [$SPACELIFT_PROMEX_LISTEN_ADDRESS]
   --scrape-timeout value, -t value  The maximum duration to wait for a response from the Spacelift API during scraping (default: 5s) [$SPACELIFT_PROMEX_SCRAPE_TIMEOUT]
```

## Version

To get version information, use the `--version` flag:

```shell
$ spacelift-promex --version
spacelift-promex version 0.0.1
```

## Available Metrics

The following metrics are provided by the exporter:

| Metric                                                     | Labels                               | Description                                                                                    |
| ---------------------------------------------------------- | ------------------------------------ | ---------------------------------------------------------------------------------------------- |
| `spacelift_public_worker_pool_runs_pending`                |                                      | The number of runs in your account currently queued and waiting for a public worker            |
| `spacelift_public_worker_pool_workers_busy`                |                                      | The number of currently busy workers in the public worker pool for this account                |
| `spacelift_public_worker_pool_parallelism`                 |                                      | The maximum number of simultaneously executing runs on the public worker pool for this account |
| `spacelift_worker_pool_runs_pending`                       | `worker_pool_id`, `worker_pool_name` | The number of runs currently queued and waiting for a worker from a particular pool            |
| `spacelift_worker_pool_workers_busy`                       | `worker_pool_id`, `worker_pool_name` | The number of currently busy workers in a worker pool                                          |
| `spacelift_worker_pool_workers`                            | `worker_pool_id`, `worker_pool_name` | The number of workers in a worker pool                                                         |
| `spacelift_worker_pool_workers_drained`                    | `worker_pool_id`, `worker_pool_name` | The number of workers in a worker pool that have been drained                                  |
| `spacelift_current_billing_period_start_timestamp_seconds` |                                      | The timestamp of the start of the current billing period                                       |
| `spacelift_current_billing_period_end_timestamp_seconds`   |                                      | The timestamp of the end of the current billing period                                         |
| `spacelift_current_billing_period_used_private_seconds`    |                                      | The amount of private worker usage in the current billing period                               |
| `spacelift_current_billing_period_used_public_seconds`     |                                      | The amount of public worker usage in the current billing period                                |
| `spacelift_current_billing_period_used_seats`              |                                      | The number of seats used in the current billing period                                         |
| `spacelift_current_stacks_count_by_state`                  | `state`                              | The number of stacks grouped by state                                                          |
| `spacelift_current_resources_count_by_drift`               | `state`                              | The number of resources by drift                                                               |
| `spacelift_current_avg_stack_size_by_resource_count`       |                                      | The average stack size by resource count                                                       |
| `spacelift_current_average_run_duration`                   |                                      | The average run duration                                                                       |
| `spacelift_current_median_run_duration`                    |                                      | The median run duration                                                                        |
| `spacelift_scrape_duration_seconds`                        |                                      | The duration in seconds of the request to the Spacelift API for metrics                        |
| `spacelift_build_info`                                     |                                      | Contains build information about the exporter (version, commit, etc)                           |

## Run metrics

The metrics above are point-in-time gauges read from the Spacelift API when
Prometheus scrapes. They cannot describe individual runs: the underlying API
fields are account-wide aggregates, so they carry no stack or space identity.

To get per-run metrics, the exporter can additionally receive
[notification policy](https://docs.spacelift.io/concepts/policy/notification-policy)
deliveries. Spacelift calls the exporter once per run that reaches a terminal
state, and the exporter turns each delivery into counters and a histogram. This
is the same mechanism the
[Datadog integration](https://docs.spacelift.io/integrations/observability/datadog)
uses, and it costs the Spacelift API nothing, because nothing is polled.

This is off by default. To enable it:

```bash
spacelift-promex serve \
  --api-endpoint https://myaccount.app.spacelift.io \
  --api-key-id "$SPACELIFT_API_KEY_ID" \
  --api-key-secret "$SPACELIFT_API_KEY_SECRET" \
  --webhook-enabled \
  --webhook-secret-file /run/secrets/promex-webhook-secret
```

Then create the notification policy and the webhook pointing at
`https://<exporter>/webhook`. There is a Terraform module in
[`contrib/terraform`](contrib/terraform) that creates both, or you can apply
[`contrib/notification-policy.rego`](contrib/notification-policy.rego) by hand
and label the webhook `prometheus`.

The webhook secret configured in Spacelift must match the exporter's
`--webhook-secret` / `--webhook-secret-file`. Every delivery is authenticated by
verifying the `X-Signature-256` HMAC over the request body, and unsigned or
mis-signed deliveries are rejected with a 401. The exporter refuses to start
with the receiver enabled but no secret set, because an unauthenticated endpoint
would let anyone who can reach it inject arbitrary run metrics.

| Metric                                              | Labels                                                                        | Description                                                                          |
| --------------------------------------------------- | ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `spacelift_runs_total`                              | `space`, `stack`, `run_type`, `final_state`, `drift_detection`, `worker_pool`  | Runs that reached a terminal state                                                   |
| `spacelift_run_duration_seconds`                    | `space`, `stack`, `run_type`                                                   | End-to-end run duration, as the sum of time spent in every run state                 |
| `spacelift_run_resource_changes_total`              | `space`, `stack`, `run_type`, `change_type`                                    | Plan-phase resource changes, by `added` / `changed` / `deleted` / `replaced`          |
| `spacelift_run_policy_evaluations_total`            | `space`, `stack`, `policy_type`, `policy_outcome`                              | Policy evaluations recorded against terminated runs                                  |
| `spacelift_webhook_deliveries_total`                | `result`                                                                      | Deliveries received, by outcome: `accepted`, `bad_signature`, `malformed`, `ignored`  |
| `spacelift_webhook_last_delivery_timestamp_seconds` |                                                                               | When the last delivery was accepted; alert on this going stale                        |
| `spacelift_webhook_payload_errors_total`            |                                                                               | Deliveries that could not be decoded                                                 |

### Things to know before relying on these

**Only terminated runs are reported.** A delivery is made when a run reaches
`FAILED`, `FINISHED`, `DISCARDED` or `STOPPED`. `CANCELED` is excluded, matching
the Datadog integration: a run that was created and cancelled never did any
work. In-flight runs produce nothing, so these metrics describe throughput and
outcomes, not what is happening right now.

**Notification policies require the Cloud tier or above.**

**Counters reset when the exporter restarts.** That is normal for a Prometheus
counter and `rate()` / `increase()` handle it. Do not build on the raw value.

**With more than one replica, each instance only sees its own deliveries.**
Whichever replica the load balancer picked gets the delivery, so aggregate
across instances with `sum without (instance) (...)` rather than reading one.
The simplest deployment is a single replica.

**Observations are timestamped on arrival.** Datadog back-dates its points to
the run's terminal timestamp; Prometheus cannot, so a run appears in the scrape
interval in which the delivery landed rather than when the run finished.

**Per-phase timing is not here.** `spacelift_run_duration_seconds` is
end-to-end. Spacelift emits OpenTelemetry spans for run internals, and those
carry sub-phase detail that state timings cannot express, such as how much of a
plan was provider downloads. Use the trace export for phase-level questions, and
the OpenTelemetry Collector's `spanmetrics` connector if you want histograms
from it in Prometheus.

**Cardinality is a function of your account.** Every distinct
`(space, stack, run_type, final_state, drift_detection, worker_pool)` is one
series, and the histogram multiplies `(space, stack, run_type)` by the number of
buckets plus two. An account with 2,000 active stacks should expect on the order
of tens of thousands of series. If that is too many, drop the `stack` label at
scrape time with a `metric_relabel_configs` rule.

The default histogram buckets span 10s to 2h, because the client library's
defaults stop at 10s and every IaC run would otherwise land in one bucket.
Override them with `--webhook-duration-buckets`.

### Example queries

```promql
# Run failure ratio per stack over the last hour.
sum by (stack) (rate(spacelift_runs_total{final_state="FAILED"}[1h]))
  / sum by (stack) (rate(spacelift_runs_total[1h]))

# 95th percentile run duration per stack over the last day.
histogram_quantile(
  0.95,
  sum by (stack, le) (rate(spacelift_run_duration_seconds_bucket[1d]))
)

# Resources destroyed per hour, which is worth alerting on.
sum(rate(spacelift_run_resource_changes_total{change_type="deleted"}[1h]))

# Deliveries are being rejected, so the secret no longer matches.
rate(spacelift_webhook_deliveries_total{result="bad_signature"}[5m]) > 0

# No delivery in six hours, so the policy or the webhook is broken.
time() - spacelift_webhook_last_delivery_timestamp_seconds > 6 * 3600
```

## Example Dashboard

If you're looking for inspiration, you can find an example Grafana dashboard
[here](examples/example-dashboard.json).
