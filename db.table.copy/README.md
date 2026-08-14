# db.table.copy

Dumps an Oracle table as pretty JSON or delimited text. Pure Go — no Oracle
Instant Client, no CGo.

## Build

```sh
go build -o db.table.copy .
```

## Configuration

HOCON, read with `github.com/go-akka/configuration`:

```hocon
database {
    url = "jdbc:oracle:thin:@//localhost:1521/aaaaaaa"
    username = "bbbb"
    password = "ccccocc"
    authenticationType = "PASSWORD"
}
```

Setting `ORACLE_PASSWORD` in the environment overrides `database.password`, so
the file can be checked in without a secret.

Supported `database.url` shapes:

| Form | Example |
| --- | --- |
| EZConnect | `jdbc:oracle:thin:@//host:1521/service` |
| EZConnect, default port | `jdbc:oracle:thin:@//host/service` |
| Host/port/service | `jdbc:oracle:thin:@host:1521/service` |
| Host/port/SID | `jdbc:oracle:thin:@host:1521:ORCL` |
| TLS | `jdbc:oracle:thin:@tcps://host:1522/service` |
| Full TNS descriptor | `jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=...)(CONNECT_DATA=(SERVICE_NAME=...)))` |

`authenticationType` must be `PASSWORD`; anything else is rejected up front
rather than failing later at connect time.

## Usage

```sh
db.table.copy [options] <table>
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-config` | `application.conf` | Path to the HOCON file |
| `-format` | `json` | `json` or `csv` |
| `-delimiter` | `,` | Single-character field delimiter for `csv` |
| `-no-header` | `false` | Omit the column-name row in `csv` |
| `-null` | `""` | Text used for NULL in `csv` |
| `-limit` | `0` | Max rows (`FETCH FIRST n ROWS ONLY`, Oracle 12c+) |
| `-where` | `""` | WHERE clause, inserted verbatim |
| `-out` | `-` | Output file, `-` for stdout |
| `-timeout` | `5m` | Overall budget for connect + read |
| `-print-dsn` | `false` | Print the derived DSN with the password redacted, then exit |

Examples:

```sh
db.table.copy -config application.conf HR.EMPLOYEES
db.table.copy -format csv -delimiter '|' EMPLOYEES > employees.psv
db.table.copy -limit 100 -where "DEPARTMENT_ID = 30" -out sample.json EMPLOYEES
db.table.copy -print-dsn
```

## Notes on correctness

- **NUMBER precision.** Oracle `NUMBER` carries up to 38 significant digits,
  which a `float64` cannot hold. Numeric columns are scanned as text and
  emitted as unquoted JSON numbers, so the exact decimal survives the round
  trip. Every other type is scanned into an `interface{}` and normalised
  afterwards, which keeps the tool working for types the driver maps in its
  own way.
- **Identifiers.** Object names cannot be bind variables, so the table name is
  concatenated into the SQL text. It is validated against
  `TABLE` / `SCHEMA.TABLE`, in unquoted or `"quoted"` form, before use.
  `-where` is *not* validated — it is inserted verbatim and should not be fed
  untrusted input.
- **Streaming.** Both writers emit rows as they arrive; memory stays flat
  regardless of table size.
- **Binary and dates.** `RAW`/`BLOB` are base64-encoded; dates and timestamps
  are formatted as RFC 3339.

## Test

```sh
go test ./...
```

Covers URL parsing (all supported shapes plus rejections), config loading,
identifier validation, numeric normalisation, and both writers.
