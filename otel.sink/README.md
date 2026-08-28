# otel.sink

A single Go binary that terminates OTLP. It accepts **traces, metrics and logs**
over OTLP/gRPC (`:4317`) and OTLP/HTTP (`:4318`), prints them, optionally writes
them to JSONL, and keeps a bounded window of recent telemetry that you can
browse in a dashboard or query as JSON.

It exists for the moment when you have instrumented an app and want to see
exactly what it emits, without standing up a collector plus Jaeger plus
Prometheus plus Loki. It is a debugging and test tool: everything it retains is
in memory and is gone on restart.

```
                                  ┌──────────────────────┐──▶ console (stdout)
   your app ──OTLP/gRPC  :4317──▶ │  flatten -> filters  │──▶ files (jsonl / json)
            └─OTLP/HTTP  :4318──▶ │                      │──▶ unix socket (jsonl stream)
                                  └──────────────────────┘──▶ ring buffer ──▶ :8080 dashboard + API
                                             │
                                             └────────────▶ another OTLP endpoint (proxy)
```

Every output implements one interface and every output sees the same records:
filters run once, ahead of all of them.

## Quick start

Requires Go 1.22 or newer (1.23+ recommended).

```bash
go mod tidy          # resolves pdata, grpc and yaml.v3
go build -o otel.sink .
./otel.sink
```

```
OTLP/gRPC listening on 0.0.0.0:4317 (plaintext)
OTLP/HTTP listening on 0.0.0.0:4318 (plaintext) (/v1/traces, /v1/metrics, /v1/logs)
dashboard and query API on http://0.0.0.0:8080
```

Point any OpenTelemetry SDK at it and you are done:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc      # or http/protobuf for :4318
export OTEL_SERVICE_NAME=my-service
./my-app
```

Then open <http://localhost:8080>.

### Verify it works without an app

Send a hand-written span over OTLP/HTTP+JSON — no SDK, no build:

```bash
curl -X POST http://localhost:4318/v1/traces \
  -H 'Content-Type: application/json' \
  -d '{"resourceSpans":[{"resource":{"attributes":[
        {"key":"service.name","value":{"stringValue":"curl-test"}}]},
       "scopeSpans":[{"scope":{"name":"manual"},"spans":[{
         "traceId":"5b8efff798038103d269b633813fc60c",
         "spanId":"eee19b7ec3c1b174","name":"hand-written span","kind":2,
         "startTimeUnixNano":"1700000000000000000",
         "endTimeUnixNano":"1700000000250000000",
         "attributes":[{"key":"http.method","value":{"stringValue":"GET"}}]
       }]}]}]}'
```

The span appears on stdout immediately and at `GET /api/traces`.

### Or with the bundled example app

`examples/emitter` is a real instrumented service: nested spans, a counter, an
up-down counter, a histogram, and `slog` logs correlated to the active trace.

```bash
cd examples/emitter
go mod tidy
go run . -endpoint localhost:4317 -service checkout
```

### Or with telemetrygen

```bash
go install github.com/open-telemetry/opentelemetry-collector-contrib/cmd/telemetrygen@latest
telemetrygen traces  --otlp-insecure --otlp-endpoint localhost:4317 --traces 5
telemetrygen metrics --otlp-insecure --otlp-endpoint localhost:4317 --metrics 5
telemetrygen logs    --otlp-insecure --otlp-endpoint localhost:4317 --logs 5
```

## Configuration

Flags override the config file; the config file overrides the defaults. Running
with no arguments at all is a valid setup.

| Flag | Default | Purpose |
|---|---|---|
| `-config` | *(none)* | Path to a YAML config file |
| `-grpc` | `0.0.0.0:4317` | OTLP/gRPC listen address |
| `-http` | `0.0.0.0:4318` | OTLP/HTTP listen address |
| `-api` | `0.0.0.0:8080` | Dashboard and query API address |
| `-verbosity` | `normal` | `none`, `basic`, `normal`, `detailed` |
| `-file-dir` | *(off)* | Write captured telemetry here; enables the file sink |
| `-file-format` | `jsonl` | `jsonl` or `json` |
| `-uds` | *(off)* | Stream to this Unix socket; enables the UDS sink |
| `-uds-mode` | `listen` | `listen` or `dial` |
| `-forward` | *(off)* | Forward every batch to this OTLP endpoint |
| `-forward-mode` | `raw` | `raw` or `filtered` |
| `-list-filters` | | Print registered filters and exit |
| `-version` | | Print version and exit |

`config.yaml` in the repo documents every option and matches the defaults
exactly. The parts worth knowing:

```yaml
otlp:
  grpc: { enabled: true, endpoint: 0.0.0.0:4317 }
  http: { enabled: true, endpoint: 0.0.0.0:4318 }
  max_request_size_mib: 16     # rejects larger exports on both transports
console:
  verbosity: normal            # none | basic | normal | detailed
file:
  enabled: false               # write traces/metrics/logs files under dir
  dir: ./data
  format: jsonl                # jsonl (append-safe) or json (single array)
store:
  max_spans: 10000             # ring buffers; memory use is bounded by these
  max_metrics: 10000
  max_logs: 10000
```

### Verbosity levels

- `basic` — one line per received batch. Use when volume is high.
- `normal` — one line per span, metric stream and log record.
- `detailed` — adds resource attributes, span attributes, span events and
  per-data-point attributes. This is the one to use when an attribute is
  missing and you need to see what actually arrived.
- `none` — silence; the dashboard and file sink still work.

### TLS and mTLS

Both receivers take the same TLS block. This matters if the emitting service
has TLS on by default, which several runtimes do.

```yaml
otlp:
  grpc:
    enabled: true
    endpoint: 0.0.0.0:4317
    tls:
      enabled: true
      cert_file: /etc/otel.sink/server.crt
      key_file: /etc/otel.sink/server.key
      client_ca_file: /etc/otel.sink/ca.crt   # omit for one-way TLS
```

Setting `client_ca_file` turns on `RequireAndVerifyClientCert`, so senders must
present a certificate signed by that CA. Minimum version is TLS 1.2.

A self-signed pair for local testing:

```bash
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout server.key -out server.crt \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
```

## Query API

All responses are JSON. Filters are case-insensitive; `name` and `contains`
match substrings, everything else is exact.

| Endpoint | Query parameters |
|---|---|
| `GET /api/stats` | — |
| `GET /api/traces` | `service`, `name`, `trace_id`, `status`, `limit` |
| `GET /api/traces/{trace_id}` | — (all spans of one trace, oldest first) |
| `GET /api/metrics` | `service`, `name`, `type`, `limit` |
| `GET /api/logs` | `service`, `contains`, `trace_id`, `min_severity`, `limit` |
| `GET /api/export` | `signal`, `format`, plus that signal's filters |
| `POST /api/reset` | — (drops retained records, keeps counters) |
| `GET /healthz` | — |

JSON has no representation for `NaN` or `±Inf`, so those encode as `null`.
This shows up on every explicit histogram: the overflow bucket's
`upper_bound` is `null`, meaning unbounded. The JSONL files use the same
convention, and otel.sink reads `null` back as `+Inf`.

`limit` defaults to 100. `min_severity` uses OTLP severity numbers: 1 TRACE,
5 DEBUG, 9 INFO, 13 WARN, 17 ERROR, 21 FATAL.

```bash
# Every errored span from the checkout service
curl -s 'localhost:8080/api/traces?service=checkout&status=Error' | jq '.spans[].name'

# One whole trace
curl -s localhost:8080/api/traces/5b8efff798038103d269b633813fc60c | jq

# Warnings and worse, mentioning "timeout"
curl -s 'localhost:8080/api/logs?min_severity=13&contains=timeout' | jq '.logs[].body'

# Latest value of one metric
curl -s 'localhost:8080/api/metrics?name=orders.processed' | jq '.metrics[0].data_points'
```

### Asserting on telemetry in tests

The API makes otel.sink usable as a test fixture: run it, run the code under
test, then assert on what arrived.

```go
resp, _ := http.Get("http://localhost:8080/api/traces?name=POST+/checkout&limit=1")
var body struct{ Spans []struct{ StatusCode string `json:"status_code"` } }
json.NewDecoder(resp.Body).Decode(&body)
if len(body.Spans) == 0 || body.Spans[0].StatusCode == "Error" { t.Fatal("checkout span missing or failed") }
```

`POST /api/reset` between test cases gives each one a clean buffer.

## Saving to files

Two ways: write continuously as telemetry arrives, or export a snapshot on
demand. Both produce the same record shapes the API returns.

### Continuous, while running

```bash
./otel.sink -file-dir ./data                     # JSONL (default)
./otel.sink -file-dir ./data -file-format json   # single JSON array per file
```

Writes `traces.*`, `metrics.*` and `logs.*` under the directory.

| | `jsonl` | `json` |
|---|---|---|
| Shape | one object per line | one array per file |
| On restart | appends | truncates and starts over |
| After a crash | still valid | array left unterminated |
| While running | tailable with `tail -f` | incomplete until exit |
| Feed to `jq` | `jq -c . file.jsonl` | `jq '.[]' file.json` |

`jsonl` is the better default: it survives `kill -9`, and `jq -s '.' traces.jsonl`
converts it to an array whenever a tool insists on one document. Use `json`
when something downstream will only accept a single JSON value.

Neither format rotates. Point `-file-dir` at a fresh directory per run, or
hand the JSONL output to logrotate.

```bash
jq -r 'select(.status_code=="Error") | "\(.service) \(.name) \(.duration_ms)ms"' data/traces.jsonl
```

### On demand, from a running sink

`GET /api/export` returns whatever is currently in the store as a download,
with the same filters as the browse endpoints. Unlike those, it defaults to
everything held rather than 100 rows, and returns records oldest first.

```bash
# Everything, one document: {exported_at, stats, traces, metrics, logs}
curl -OJ localhost:8080/api/export

# One signal, filtered
curl -o errors.json 'localhost:8080/api/export?signal=traces&status=Error'
curl -o checkout.jsonl 'localhost:8080/api/export?signal=logs&format=jsonl&service=checkout'
```

| Parameter | Values | Default |
|---|---|---|
| `signal` | `traces`, `metrics`, `logs`, `all` | `all` |
| `format` | `json`, `jsonl` | `json` |
| filters | same as the browse endpoint for that signal | none |
| `limit` | max records | everything held |

`format=jsonl` requires a single signal, since one file of bare lines cannot
carry three record types. The dashboard's **Download JSON** / **Download JSONL**
buttons hit this endpoint with whatever filters are on screen.

## Streaming to a Unix socket

```bash
./otel.sink -uds /tmp/otel.sink.sock
nc -U /tmp/otel.sink.sock | jq 'select(.signal=="traces") | .record.name'
```

One JSON object per line, so a reader can attach and detach at any time. By
default each line is enveloped so a single socket carries all three signals:

```json
{"signal":"traces","record":{"trace_id":"...","name":"POST /checkout",...}}
```

Set `uds.envelope: false` for bare records when the consumer already knows
which signal it is reading.

Two modes:

- **`listen`** (default) — otel.sink creates the socket and broadcasts to
  every attached reader. A stale socket file from a killed run is replaced;
  a *regular* file at that path is an error rather than something to delete.
- **`dial`** — otel.sink connects to a socket someone else created, and
  reconnects if that peer restarts. Use this when a sidecar or agent owns
  the socket.

**Writes never block the export path.** Batches go to a bounded queue
(`uds.buffer`, default 1024) drained by a background goroutine. With no
reader attached, or a reader that cannot keep up, batches are dropped and
counted, and the total is logged on shutdown. A debugging tap must not be
able to stall the service being debugged — which is the opposite of the
console and file sinks, where back-pressure is the correct behaviour.

The socket is created mode `0660`, so a reader needs to share the group.

## Forwarding to another collector (proxy mode)

otel.sink can sit in front of a real backend: capture everything locally and
pass it on.

```bash
./otel.sink -forward collector.internal:4317
```

```yaml
forward:
  enabled: true
  endpoint: collector.internal:4317
  protocol: grpc              # or http, where signal paths are appended
  mode: raw                   # raw | filtered
  headers: { authorization: "Bearer ..." }
  compression: gzip
  fail_open: true
```

Point your services at otel.sink instead of the collector and nothing else
changes: batches are forwarded as **pdata, not rebuilt** from otel.sink's
flattened records, so span links, exponential histogram bucket layouts,
dropped-attribute counts and schema URLs all survive the hop intact.

### `raw` vs `filtered` — read this if you use filters

| | `raw` (default) | `filtered` |
|---|---|---|
| What goes upstream | the batch exactly as received | what the filter chain kept |
| Filter drops apply | no | yes |
| Attribute edits apply | no | yes |
| Cost | none | one extra pass per batch |

**`raw` forwards unfiltered data.** That is the right default for a
transparent proxy, but it means a `redact` filter protects your local files
and dashboard while the unredacted original still goes upstream. If a filter
exists for privacy rather than for noise, use `mode: filtered`. otel.sink
logs a warning at startup when filters are configured alongside `mode: raw`.

In `filtered` mode the pdata batch is reconciled against the filter chain's
decisions: dropped records are removed (along with any scope or resource left
empty) and attribute edits are copied back, so upstream sees exactly what the
local outputs see.

### When the upstream is down

`fail_open: true` (default) logs the failure and reports success to the
sender, so a broken collector does not stop local capture — otel.sink is a
debugging tool first. Set it to `false` for a strict proxy: the error
propagates back to the sending SDK, which then applies its own retry and
drop policy, which is where that decision belongs.

Forwarding is synchronous, so a slow upstream slows the export call. Each
attempt is bounded by `timeout` and retried `retries` times.

### TLS to the upstream

```yaml
forward:
  insecure: false
  ca_file: /etc/ssl/collector-ca.crt
  cert_file: /etc/otel.sink/client.crt   # mTLS
  key_file: /etc/otel.sink/client.key
```

## Custom processing with filters

Filters transform or drop records between flattening and the outputs. They
run once per batch, so every sink — and the dashboard — sees the same data.

```yaml
filters:
  - name: drop_names
    options: { contains: /health,/metrics }
  - name: redact
    options: { keys: db.statement,http.url }
  - name: sample
    options: { ratio: "0.1" }
```

`otel.sink -list-filters` prints what is available.

| Filter | Options | Applies to |
|---|---|---|
| `redact` | `keys=a,b` `[value=]` | all — replaces those attribute values |
| `drop_names` | `contains=a,b` | spans, metrics |
| `min_duration` | `ms=50` | spans |
| `min_severity` | `severity=warn` | logs |
| `services` | `allow=` or `deny=` | all |
| `sample` | `ratio=0.1` | spans |
| `errors_only` | — | spans, logs |

`sample` hashes the trace ID rather than deciding per span, so a kept trace
keeps all of its spans and the waterfall stays intact.

### Writing your own

Filters are ordinary Go, compiled in — register one in an `init()` and name
it in the config. `internal/filter/custom.go` has worked examples; the
simplest is a per-record function, where a nil signal passes through:

```go
func init() {
	filter.RegisterFuncs("drop_internal_ips", filter.Funcs{
		Span: func(s model.Span) (model.Span, bool) {
			host, _ := s.Attributes["net.peer.name"].(string)
			return s, !strings.HasPrefix(host, "10.")
		},
	})
}
```

Return `false` to drop a record; mutate and return `true` to edit it. For
options from the config, register a factory taking `map[string]string`. For a
decision that needs the whole batch — deduplication, rate limiting — implement
the three-method `filter.Filter` interface directly and guard any state with a
mutex, since batches arrive concurrently.

## Adding an output

Every output satisfies one interface:

```go
type Sink interface {
	Name() string
	Spans([]model.Span)
	Metrics([]model.Metric)
	Logs([]model.Log)
	Close() error
}
```

The store, console, file and UDS sinks are all just implementations. To add
one (Kafka, a socket, an HTTP webhook), implement those five methods and
append it to the slice in `main.go`; the Fanout owns the lifecycle and closes
it on shutdown. Sinks run inline and may retain nothing from the slice they
are handed — copy anything kept past the call.

## Docker

```bash
docker build -t otel.sink .
docker run --rm -p 4317:4317 -p 4318:4318 -p 8080:8080 otel.sink -verbosity detailed
```

Senders in other containers should use the container name as the host
(`http://otel.sink:4317`), not `localhost`.

## How it fits together

```
receiver/grpc.go ─┐                                                  ┌─▶ store (ring buffers) ──▶ api
                  ├─▶ sink.Fanout ─▶ model.Flatten* ─▶ filter.Chain ─┼─▶ console
receiver/http.go ─┘                │                                 ├─▶ file
                                   │                                 └─▶ uds
                                   └─▶ forwarder (pdata, not flattened) ─▶ upstream OTLP
```

- `internal/model` is the only package that touches pdata. Everything
  downstream works with plain structs, which is why the JSON in the API, the
  JSONL files and the console output all agree.
- `sink.Fanout` flattens each batch once, runs the filter chain once, and
  hands the survivors to every sink. Sinks run inline, so a slow sink
  back-pressures the sender rather than dropping data quietly. A sink that
  must not block (the UDS stream) buffers internally instead.
- `filter.Chain` sits between flattening and the outputs, which is why
  filtering affects the dashboard as well as the files.
- Forwarding deliberately bypasses the flattened model: `model.Span` and
  friends are a lossy view built for display and JSON, so rebuilding pdata
  from them would quietly degrade proxied telemetry. `filtered` mode instead
  reconciles filter decisions back onto the original batch, using the
  per-batch `Index` that flattening assigns.
- `store` uses fixed-capacity ring buffers, so a long-running sink cannot grow
  without bound; the oldest records are overwritten.

Three dependencies: `go.opentelemetry.io/collector/pdata` for the OTLP data
model and both the server and client gRPC stubs, `google.golang.org/grpc`,
and `gopkg.in/yaml.v3`. Forwarding adds none: the OTLP client ships with
pdata, and the HTTP transport is `net/http` posting the same protobuf bytes.

## Troubleshooting

**Connection refused / reset, nothing in the logs.** Almost always TLS
mismatch: the SDK is speaking TLS to a plaintext listener or the reverse. Set
`OTEL_EXPORTER_OTLP_INSECURE=true` on the sender to rule it out, or configure
the TLS block above.

**Nothing arrives, no errors either.** Most SDKs batch and flush on an interval
(commonly 5s for metrics, 1–5s for spans) and drop everything if the process
exits without a flush. Make sure the app calls `Shutdown` on its providers.

**415 Unsupported Media Type.** OTLP/HTTP requires
`Content-Type: application/x-protobuf` or `application/json`. `gzip` in
`Content-Encoding` is supported.

**`http: request body too large`.** Raise `otlp.max_request_size_mib`, or lower
the sender's batch size.

**`conflicting Schema URL` at startup (in your own app, not otel.sink).**
`resource.Merge` rejects two resources that declare different semconv schema
URLs, and `resource.Default()` declares whichever version your SDK was built
against. Pass an empty schema URL in your own `resource.NewWithAttributes`
call rather than pinning `semconv.SchemaURL` to match. See
`examples/emitter/main.go`.

**Which port?** `4317` is gRPC only, `4318` is HTTP only. Sending protobuf over
HTTP to 4317, or gRPC to 4318, fails in confusing ways. `OTEL_EXPORTER_OTLP_PROTOCOL`
must match the port.

**Is the server even up?** `curl localhost:4318/healthz` and
`curl localhost:8080/healthz`. For gRPC, the server has reflection enabled:
`grpcurl -plaintext localhost:4317 list`.

## Testing and development

```bash
make test      # unit tests
make vet
make build
make emitter   # run the example instrumented app against a running sink
```

## Limitations

- In-memory only; no persistence, no querying across restarts. The file sink
  and `/api/export` are how data leaves the process.
- Forwarding goes to one upstream endpoint. For fan-out to several backends,
  point otel.sink at an OpenTelemetry Collector and let it route.
- Exponential histograms are stored as count/sum/min/max; the scaled bucket
  layout is not expanded.
- The API and dashboard are unauthenticated and CORS-open. Bind them to
  localhost or a trusted network.
- Filters are compiled in, not loaded at runtime. Adding one means a rebuild.
- The UDS stream drops rather than blocks; it is a tap, not a durable
  transport. Use the file sink when you cannot lose records.
