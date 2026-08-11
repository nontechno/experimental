# db.schema.check

Reads a HOCON-style config file, connects to the Oracle database it names, and
reports the schema: tables, columns, constraints, indexes, views and sequences.

Pure Go — it uses [go-ora](https://github.com/sijms/go-ora), so there is no
Oracle Instant Client or `CGO` dependency. `go build` produces a static binary.

## Build and run

```
go mod tidy
go build -o db.schema.check .
./db.schema.check -config app.conf
```

## Configuration

```hocon
database {
    url                = "jdbc:oracle:thin:@//localhost:1521/aaaaaaa"
    username           = "bbbb"
    password           = "ccccocc"
    authenticationType = "PASSWORD"
}
```

Optional keys:

```hocon
database {
    schema = "HR"          # schema to report on; defaults to the session's own
    options {              # passed through to the go-ora connection URL
        "TRACE FILE" = "/tmp/trace.log"
        SSL          = "TRUE"
    }
}
```

The parser supports nested blocks, dotted keys (`database.schema = "HR"`),
`=` or `:` separators, quoted and bare values, `#`, `//` and `/* */` comments,
and `${VAR}` / `${?VAR}` environment substitution. Keeping the password out of
the file is usually the right call:

```hocon
password = ${ORACLE_PASSWORD}
```

`${VAR}` fails loudly if the variable is unset; `${?VAR}` resolves to empty.

### Supported URL forms

| Form | Example |
| --- | --- |
| Service name | `jdbc:oracle:thin:@//host:1521/service` |
| Service name, no `//` | `jdbc:oracle:thin:@host:1521/service` |
| SID | `jdbc:oracle:thin:@host:1521:ORCL` |
| TNS descriptor | `jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=h)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=s)))` |

`PROTOCOL=TCPS` in a descriptor turns on TLS. Bare TNS aliases
(`jdbc:oracle:thin:@myalias`) are rejected, since resolving one needs a
`tnsnames.ora`; use an explicit host/port URL instead.

`authenticationType` accepts `PASSWORD` (the default) and `WALLET`. Wallet auth
additionally needs `database.options.WALLET` pointing at the wallet directory.

## Flags

```
-config string     path to the configuration file (default "app.conf")
-schema string     schema to report on (default: the session's current schema)
-format string     output format: text or json (default "text")
-out string        output file, or - for stdout (default "-")
-timeout duration  overall timeout for connecting and querying (default 2m0s)
-v                 log the resolved connection target to stderr
```

Schema names are upper-cased to match the data dictionary unless you
double-quote them: `-schema '"MixedCase"'`.

## Output

Text mode:

```
Schema:   HR
Endpoint: localhost:1521/aaaaaaa
User:     HR
Server:   Oracle Database 19c Enterprise Edition 19.3.0.0.0
Objects:  1 tables, 1 views, 1 sequences

========================================================================
TABLES (1)
========================================================================

EMPLOYEES  [~107 rows]
----------------------
  employees of the firm
  #  COLUMN         TYPE               NULL      DEFAULT
  1  EMPLOYEE_ID    NUMBER(6)          NOT NULL  IDENTITY
  2  FIRST_NAME     VARCHAR2(20 CHAR)
  3  EMAIL          VARCHAR2(25 CHAR)  NOT NULL
  4  HIRE_DATE      DATE               NOT NULL  SYSDATE
  5  SALARY         NUMBER(8,2)
  6  DEPARTMENT_ID  NUMBER(4)
    -- EMAIL: corporate address
  PK   EMP_EMP_ID_PK (EMPLOYEE_ID)
  UQ   EMP_EMAIL_UK (EMAIL)
  FK   EMP_DEPT_FK (DEPARTMENT_ID) -> DEPARTMENTS(DEPARTMENT_ID) ON DELETE SET NULL
  CK   EMP_SALARY_MIN CHECK (salary > 0)
  IX   EMP_NAME_IX (LAST_NAME, FIRST_NAME DESC)
  IXU  EMP_EMAIL_UK (EMAIL)
```

`-format json` emits the same model as structured JSON, which is what you want
if you are diffing schemas between environments:

```
./db.schema.check -format json -out prod.json
diff <(jq -S . dev.json) <(jq -S . prod.json)
```

## Privileges

Everything is read from the `ALL_*` dictionary views, so a plain user sees its
own schema with no extra grants. Reporting on someone else's schema needs
`SELECT` on those objects, or `SELECT_CATALOG_ROLE`.

Optional pieces degrade rather than fail: if `PRODUCT_COMPONENT_VERSION`,
`ALL_TAB_COMMENTS` or `ALL_SEQUENCES` are not readable, those sections are
simply omitted.

## Notes on the dictionary queries

A few Oracle-specific wrinkles the code works around:

- `ALL_TAB_COLS.DATA_DEFAULT` and `ALL_CONSTRAINTS.SEARCH_CONDITION` are `LONG`
  columns. The queries select `SEARCH_CONDITION_VC` (12.1+) where available and
  fall back to omitting the default/condition rather than failing outright.
- `IDENTITY_COLUMN` only exists from 12.1, so there is an 11g fallback query
  against `ALL_TAB_COLUMNS`.
- `NUM_ROWS` is an optimizer estimate from the last stats gather, not a count.
  It is labelled `~` in the report for that reason.
- Check constraints with system-generated names are filtered out, since those
  are mostly the implicit `NOT NULL` constraints already shown per column.
- Only the first `ADDRESS` in a TNS descriptor is used; failover address lists
  are not expanded.

## Tests

```
go test ./...
```

Covers the config parser (comments, dotted keys, env substitution, error
cases), the JDBC URL parser, and column type rendering. The dictionary queries
themselves need a live database.
