# dbnfs

A read-only NFSv3 server that shows Oracle Database catalog information as
files. It is a single static Go binary (no CGO, no Oracle Instant Client)
and runs on macOS and Linux. Clients use the operating system's built-in NFS
client, so nothing extra is installed on macOS, and Linux only needs the
standard NFS mount helper.

```
$ ls /mnt/oradb
README.txt  function  package  schema  sequence  sproc  synonym  table  trigger  view

$ ls /mnt/oradb/table
HR.COUNTRIES  HR.DEPARTMENTS  HR.EMPLOYEES  HR.JOBS  HR.LOCATIONS  HR.REGIONS

$ ls /mnt/oradb/table/HR.EMPLOYEES
columns.tsv  constraints.tsv  ddl.sql  indexes.tsv  rows.csv

$ column -t -s $'\t' /mnt/oradb/table/HR.EMPLOYEES/columns.tsv | head -3
column_id  column_name  data_type  char_length  data_precision  data_scale  nullable
1          EMPLOYEE_ID  NUMBER                  6               0           N
2          FIRST_NAME   VARCHAR2   20 BYTE                                  Y
```

## Layout

| Path | Contents |
| --- | --- |
| `/README.txt` | layout summary |
| `/schema/<OWNER>/objects.tsv` | every object in the schema, with status and timestamps |
| `/table/<OWNER>.<NAME>/` | `columns.tsv`, `constraints.tsv`, `indexes.tsv`, `ddl.sql`, `rows.csv` |
| `/view/<OWNER>.<NAME>/` | `columns.tsv`, `ddl.sql`, `rows.csv` |
| `/sproc/<OWNER>.<NAME>/` | standalone procedures: `arguments.tsv`, `source.sql` |
| `/function/<OWNER>.<NAME>/` | standalone functions: `arguments.tsv`, `source.sql` |
| `/package/<OWNER>.<NAME>/` | `arguments.tsv` (every subprogram), `spec.sql`, `body.sql` |
| `/sequence/<OWNER>.<NAME>/` | `info.txt` |
| `/trigger/<OWNER>.<NAME>/` | `info.txt`, `source.sql` |
| `/synonym/<OWNER>.<NAME>/` | `info.txt` |

**File formats**

- **Names.** Object names are shown exactly as stored in the dictionary. The characters `%`, `/` and `.` inside a name are percent-encoded (`%25`, `%2F`, `%2E`), so every object has exactly one unambiguous path. For example, the Java class `SYS."java/lang/Object"` appears as `SYS.java%2Flang%2FObject`.
- **`*.tsv`.** The first line is a header. NULL is an empty cell. Tab, newline, CR and backslash inside a value are written as `\t`, `\n`, `\r` and `\\`.
- **`rows.csv`.** RFC 4180 CSV holding at most `export.maxRows` rows, in no particular order (there is no `ORDER BY`).
  - Numbers, dates and timestamps are converted to text in SQL. Dates use ISO-8601 (`2024-05-01T13:45:00`), and the decimal separator is always `.`.
  - CLOB/NCLOB values are cut to `export.lobChars` characters on the server.
  - BLOBs appear as `<BLOB n bytes>`.
  - LONG, BFILE, XMLTYPE, object types and similar columns appear as `<TYPE>`.
- **`ddl.sql`** comes from `DBMS_METADATA.GET_DDL`.
- **`source.sql`, `spec.sql` and `body.sql`** are assembled from `ALL_SOURCE` and wrapped as `CREATE OR REPLACE … /`.
- **Errors.** If a file cannot be generated (missing privilege, timeout), it contains the error message instead, and the error is also logged. A single bad file never hides the rest of a directory.

## Build

Requires Go 1.23 or newer.

```sh
cd dbnfs
go test ./...
CGO_ENABLED=0 go build -trimpath -o dbnfs ./cmd/dbnfs
```

To cross-compile from any machine, for example for an Apple Silicon Mac and an x86-64 Linux host:

```sh
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o dbnfs-darwin-arm64 ./cmd/dbnfs
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -o dbnfs-linux-amd64  ./cmd/dbnfs
```

## Database account

What appears under the mount is exactly what the account can see through the
`ALL_*` dictionary views. Create a dedicated account with no write privileges.

```sql
CREATE USER dbnfs_ro IDENTIFIED BY "a-long-random-password";
GRANT CREATE SESSION TO dbnfs_ro;

-- Data for rows.csv (and visibility in ALL_TABLES/ALL_VIEWS).
-- READ (12c+) is preferable to SELECT: it cannot lock rows.
GRANT READ ON hr.employees TO dbnfs_ro;

-- ddl.sql for objects the account does not own.
GRANT SELECT_CATALOG_ROLE TO dbnfs_ro;
```

`ALL_SOURCE` and `ALL_ARGUMENTS` only show PL/SQL the account may execute (or
owns), so grant `EXECUTE` on packages whose source you want to browse.

## Configuration

```sh
cp dbnfs.conf.example dbnfs.conf
chmod 600 dbnfs.conf
$EDITOR dbnfs.conf
```

The minimum is the `database` block:

```hocon
database {
    url = "jdbc:oracle:thin:@//localhost:1521/FREEPDB1"
    username = "DBNFS_RO"
    password = "..."
    authenticationType = "PASSWORD"
}
```

| Key | Default | Notes |
| --- | --- | --- |
| `database.url` | required | `jdbc:oracle:thin:@//host[:port]/service`, `@host:port/service`, or `@host:port:SID`. IPv6 as `[::1]`. TNS descriptors, aliases and URL parameters are rejected. |
| `database.username` | required | `SYS` is refused (the driver would open a SYSDBA session). |
| `database.password` | — | Literal password. Requires the file to be mode `600` (or `400`). |
| `database.passwordEnv` | — | Name of an environment variable holding the password. Use instead of `password`. |
| `database.authenticationType` | `PASSWORD` | Only `PASSWORD` is supported. |
| `database.maxConnections` | `4` | Sessions in the pool (1–64). |
| `nfs.listen` | `127.0.0.1:12049` | TCP address to serve on. |
| `nfs.allowRemote` | `false` | Must be `true` to listen on a non-loopback address. |
| `export.schemas` | all non Oracle-maintained | Array of schema names, e.g. `["HR", "SALES"]`. The default needs 12.1+; on 11g list schemas explicitly. |
| `export.maxRows` | `100` | Rows in `rows.csv` (0–1,000,000). `0` removes `rows.csv`. |
| `export.maxObjects` | `10000` | Entries per directory; a warning is logged when a listing is cut. |
| `export.lobChars` | `1000` | CLOB/NCLOB prefix length (0–1000). |
| `export.maxFileBytes` | `8388608` | Hard cap per file; CSV/TSV stop at a row boundary. |
| `export.cacheTTL` | `60s` | Go duration syntax (`90s`, `5m`). |
| `export.queryTimeout` | `30s` | Deadline for each database round trip. |

Other keys in the file are ignored, so a JVM `application.conf` with a
matching `database` block can often be used directly.

The parser implements a strict **subset of HOCON**:

- **Supported:**
  - objects, dotted keys and `#`/`//` comments
  - quoted strings with JSON escapes, and raw `"""triple-quoted"""` strings
  - arrays of scalars
  - `${VAR}` / `${?VAR}` environment substitution as a whole value
- **Rejected with a line number:** `include`, `+=`, value concatenation and config-path substitutions. None of these ever silently produce a different value.

Check the configuration and privileges without serving anything:

```sh
./dbnfs -config dbnfs.conf -check
```

## Run

```sh
./dbnfs -config dbnfs.conf
```

On startup it connects, lists schemas (failing fast on privilege problems),
and prints the mount command for the current OS. Stop it with Ctrl-C.
Add `-debug` for per-file timing and NFS request logs.

Unmount clients **before** stopping the server; otherwise processes using the
mount wait for the NFS timeout (the `soft` option below bounds that wait).

## Mount on macOS

Nothing needs to be installed.

```sh
mkdir -p ~/oradb
sudo mount -t nfs \
  -o vers=3,tcp,port=12049,mountport=12049,nolocks,rdonly,soft,nobrowse \
  127.0.0.1:/ ~/oradb

ls ~/oradb/table
```

What the options do:

- `vers=3,tcp`: the server speaks NFSv3 over TCP only.
- `port=…,mountport=…`: there is no portmapper, so both ports must be given explicitly.
- `nolocks`: the server has no lock manager.
- `rdonly`: the export is read-only anyway; this makes the client refuse writes up front.
- `soft`: calls fail with an error instead of hanging if dbnfs is stopped.
- `nobrowse`: keeps the mount off the Finder sidebar and desktop.

Use `127.0.0.1` rather than `localhost`: macOS may resolve `localhost` to `::1`, while dbnfs listens on IPv4 by default.

Unmount:

```sh
sudo umount ~/oradb
# if it reports "Resource busy":
sudo diskutil unmount force ~/oradb
```

macOS notes:

- If `ls` says *Operation not permitted*, allow your terminal app under **System Settings → Privacy & Security → Files and Folders → Network Volumes**.
- Finder tries to write `.DS_Store` files. Those writes fail harmlessly with a read-only error. The server also exposes a `.metadata_never_index` marker so Spotlight skips the volume, but browsing large schemas in Finder still issues many queries. The terminal is the better client.

## Mount on Linux

Install the NFS mount helper once:

```sh
sudo apt install nfs-common     # Debian, Ubuntu
sudo dnf install nfs-utils      # Fedora, RHEL, Rocky, Alma
sudo pacman -S nfs-utils        # Arch
```

Mount:

```sh
sudo mkdir -p /mnt/oradb
sudo mount -t nfs \
  -o vers=3,proto=tcp,port=12049,mountport=12049,mountproto=tcp,nolock,noacl,ro,soft,timeo=100,retrans=2 \
  127.0.0.1:/ /mnt/oradb

ls /mnt/oradb/table
```

The options mirror the macOS ones:

- `mountproto=tcp` together with explicit ports avoids any rpcbind lookup, so `rpcbind` does not need to be running.
- `nolock` skips the lock manager (no `rpc.statd` needed).
- `noacl` skips the NFSACL side protocol, which the server does not implement.
- `timeo=100,retrans=2` makes a stopped server surface as an I/O error after about 30 seconds instead of hanging.

Unmount:

```sh
sudo umount /mnt/oradb
# if a process still holds it:
sudo umount -l /mnt/oradb
```

To let a regular user mount it without `sudo`, add an `/etc/fstab` entry:

```
127.0.0.1:/  /mnt/oradb  nfs  vers=3,proto=tcp,port=12049,mountport=12049,mountproto=tcp,nolock,noacl,ro,soft,timeo=100,retrans=2,noauto,user  0 0
```

Then run `mount /mnt/oradb` and `umount /mnt/oradb`.

## Safety model

Several independent layers keep this read-only.

1. **Database privileges.** The account should hold only `CREATE SESSION` and read grants. This is the real boundary; everything below is defence in depth.
2. **Read-only transactions.** Every statement runs in a transaction that begins with `SET TRANSACTION READ ONLY` and always ends in `ROLLBACK`. An attempt to modify data (for example a function called from a view) fails with ORA-01456.
   - The single exception is `DBMS_METADATA.GET_DDL`. If it raises ORA-01456 because it needs session-private work tables, that one call is retried in an ordinary transaction, which is still rolled back. Autonomous transactions inside PL/SQL you query are, by design, outside any of this.
3. **No SQL built from input.**
   - Every dictionary lookup uses bind variables, including the configured schema names.
   - The only identifiers placed in SQL text are the owner, table and column names in `rows.csv`. Those are read back from `ALL_TAB_COLUMNS` and double-quoted, and a name containing `"` is refused.
   - Names from NFS paths are only ever matched against cached dictionary listings.
4. **Read-only filesystem.** The filesystem reports no write capability, so go-nfs answers every mutating RPC (WRITE, CREATE, MKDIR, REMOVE, RENAME, SETATTR, …) with `NFS3ERR_ROFS`, and the filesystem methods refuse writes as well.
5. **Network exposure.** NFSv3 here uses `AUTH_NULL`, meaning no authentication. The listener is loopback-only unless `nfs.allowRemote = true`. Anyone who can connect can read everything the database account can read.
6. **Secrets.**
   - A config file containing `database.password` must not be readable by group or others.
   - The password is never logged. It is placed in the driver URL with proper escaping.
   - `passwordEnv` keeps it out of the file entirely.
7. **Resource limits.**
   - Per-query deadline (`queryTimeout`).
   - Row, object and byte caps.
   - A bounded connection pool.
   - An in-memory cache with a 256 MiB budget.

## Behaviour and limitations

- **Caching.** Listings and files are generated on first access and reused for `cacheTTL`. `ls -l` inside an object directory generates every file in it, because NFS returns exact sizes with directory entries. A file's modification time is the moment it was generated.
- **Consistency.** A file never changes while it is cached. If a very large file is being read exactly when its cache entry expires, a reader may see bytes from two generations. The client notices the changed mtime, and re-reading gives a consistent copy.
- **File handles** are derived from the path. After restarting dbnfs, clients get *Stale file handle* for anything but the root. `cd /` and back, or remount.
- **No streaming.** `rows.csv` is a bounded sample, not an export tool. For full extracts use SQLcl or Data Pump.
- **Oracle versions.**
  - The dictionary queries target 12.1 and newer (`ORACLE_MAINTAINED`, `READ` privilege).
  - 11.2 should work with `export.schemas` set, but has not been tested.
  - Oracle Autonomous Database wallets (TCPS) are not supported.
- **Other databases.** Only Oracle is implemented. The filesystem layer (`internal/dbfs`) is database-agnostic, so another database needs only a new `dbfs.Source`.

## Troubleshooting

| Symptom | Cause / fix |
| --- | --- |
| `mount: ... Connection refused` | dbnfs is not running, or the port or address differs from `nfs.listen`. |
| `mount.nfs: requested NFS version or transport protocol is not supported` | `vers=3` or `proto=tcp` is missing. |
| `mount.nfs: No such device` | The Linux kernel has no NFS client module (common in containers). Load it with `sudo modprobe nfs`. |
| Directory is empty | The account cannot see those objects in `ALL_*` views; check grants and `export.schemas`. Run `-check`. |
| `ddl.sql` holds `ORA-31603` | `DBMS_METADATA` needs `SELECT_CATALOG_ROLE` for objects the account does not own. |
| `Input/output error` on a directory | The listing query failed (connection lost, timeout); see the dbnfs log. |
| `Stale file handle` | dbnfs was restarted; `cd` out and back, or remount. |
| Commands hang after dbnfs stopped | The mount was made without `soft`; force-unmount. |

## Project layout

```
cmd/dbnfs          flags, startup checks, signal handling
internal/config    HOCON-subset parser, JDBC URL parser, validation
internal/dbfs      read-only go-billy filesystem, cache, stable NFS handles
internal/oracle    go-ora connection, dictionary queries, TSV/CSV rendering
```

Dependencies:

- **`github.com/sijms/go-ora/v2`**: pure-Go Oracle driver.
- **`github.com/willscott/go-nfs`** and **`github.com/go-git/go-billy/v5`**: NFSv3 server and filesystem interface.
- **`github.com/willscott/go-nfs-client`**: tests only.
