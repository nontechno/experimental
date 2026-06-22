# sse.executor

Subscribes to an SSE stream and runs a shell script for every matching event.  
Zero non-stdlib dependencies. Requires Go 1.22+.

## Build

```bash
go build -o sse.executor .
```

## Run

```bash
./sse.executor -config config.json
```

## Config reference

| Field | Type | Default | Description |
|---|---|---|---|
| `server_url` | string | — | Base URL; topic is appended as `/<topic>` |
| `topic` | string | — | Topic / channel name |
| `url_template` | string | — | Overrides URL composition; use `{topic}` placeholder |
| `headers` | map | — | Static HTTP headers (auth, etc.) |
| `script` | string | — | Executable to run on each event |
| `script_args` | []string | `[]` | Prepended before the event-data argument |
| `script_timeout_sec` | int | 30 | Hard kill timeout for the script |
| `event_types` | []string | `[]` | Filter — empty means all types |
| `log_level` | string | `"info"` | `debug` / `info` / `warn` / `error` |
| `reconnect.initial_delay_sec` | float | 1 | First reconnect wait |
| `reconnect.max_delay_sec` | float | 60 | Back-off ceiling |
| `reconnect.multiplier` | float | 2.0 | Exponential back-off factor |
| `reconnect.reset_after_sec` | float | 30 | Stable-connection threshold to reset back-off |
| `reconnect.max_attempts` | int | 0 | 0 = unlimited |

## Script contract

The script receives the raw event data as **`$1`** and via env vars:

| Var | Content |
|---|---|
| `SSE_EVENT_DATA` | Raw data string (same as `$1`) |
| `SSE_EVENT_TYPE` | Event type field (`message` if unset) |
| `SSE_EVENT_ID` | Event id field (empty string if unset) |

Scripts run **concurrently** (one goroutine per event) so multiple events can
be processed in parallel. Make scripts idempotent or add locking if needed.

## URL construction

If `url_template` is set it takes priority:

```json
"url_template": "https://host/sse?channel={topic}"
```

Otherwise the URL is `server_url/topic`.
