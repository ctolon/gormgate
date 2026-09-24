# gormgate integration tests

Separate Go module (`github.com/ctolon/gormgate/itest`) that runs gormgate
against real database servers in Docker. All test files use the build tag
`integration`.

## Quick start

```sh
make -C itest up            # start all servers, wait until healthy (~20 s)
make -C itest smoke         # smoke tests for every host-runnable vendor
make -C itest oracle-smoke  # Oracle smoke test inside the runner container
make -C itest down          # stop everything (data is on tmpfs and is lost)
```

Other targets: `smoke-all` (up + smoke + oracle-smoke), `up-<service>`,
`smoke-<key>` (e.g. `make -C itest smoke-cockroach`, starts only that
service), `oracle-runner` (build the runner image), `ps`.
`TESTFLAGS=-v` passes extra flags to `go test`.

Without make:

```sh
docker compose -f itest/compose.yaml up -d --wait pg18
cd itest && go test -tags integration -run 'TestSmoke/^pg18$' -v ./...
```

## Services

| key / service | image                                      | host port (127.0.0.1)  | admin credentials            |
|---------------|--------------------------------------------|------------------------|------------------------------|
| pg14          | postgres:14                                | 15414                  | gormgate / gormgate          |
| pg18          | postgres:18                                | 15418                  | gormgate / gormgate          |
| mysql80       | mysql:8.0                                  | 13380                  | root / gormgate              |
| mysql84       | mysql:8.4                                  | 13384                  | root / gormgate              |
| mariadb106    | mariadb:10.6                               | 13406                  | root / gormgate              |
| mariadb118    | mariadb:11.8                               | 13418                  | root / gormgate              |
| tidb          | pingcap/tidb:v8.5.1 (unistore)             | 14000                  | root / (none)                |
| cockroach     | cockroachdb/cockroach:latest-v25.2         | 15426 (SQL), 18080 (UI)| root, insecure               |
| mssql         | mcr.microsoft.com/mssql/server:2022-latest | 14330                  | sa / GormGate-Pass123        |
| oracle        | gvenzl/oracle-free:23-slim                 | 15210 (FREEPDB1)       | gormgate / gormgate; SYSTEM / GormGate123 |
| gauss         | opengauss/opengauss-server:latest          | 15432                  | gormgate / Gormgate-123      |
| clickhouse    | clickhouse/clickhouse-server:25.8          | 19000 (native), 18123 (HTTP) | gormgate / gormgate    |
| sqlite        | in-process, mattn/go-sqlite3 (cgo)         | -                      | -                            |
| sqlite-purego | in-process, glebarez/sqlite (pure Go)      | -                      | -                            |

Every server except Oracle keeps its data on tmpfs. All host ports are below
32768, i.e. outside Linux' default ephemeral range (32768-60999): an earlier
layout on 33xxx/34000/543xx once failed `up` with `failed to bind host port
127.0.0.1:34000/tcp: address already in use`.

## The `dbtest` helper

```go
db, info := dbtest.Open(t, "pg18") // *gorm.DB on a brand-new database
// info.Family ("postgresql"), info.Dialect, info.Version, info.Database, info.DSN
```

`Open` connects with the admin DSN, creates an isolated database with a random
`gg_<hex>` name, returns a `*gorm.DB` (proper gorm dialector per vendor)
connected to it and drops it in `t.Cleanup`:

| family                         | isolation unit                                                   |
|--------------------------------|------------------------------------------------------------------|
| postgresql                     | `CREATE DATABASE`; dropped `WITH (FORCE)`                        |
| cockroachdb                    | `CREATE DATABASE`; dropped `CASCADE`                             |
| gaussdb                        | `CREATE DATABASE ... DBCOMPATIBILITY 'PG'`; backends terminated, then dropped |
| mysql, mariadb, tidb           | `CREATE DATABASE`                                                |
| mssql                          | `CREATE DATABASE`; `SET SINGLE_USER WITH ROLLBACK IMMEDIATE` before drop |
| oracle                         | `CREATE USER` (quota on USERS + create privileges); sessions killed, `DROP USER ... CASCADE` |
| clickhouse                     | `CREATE DATABASE`; `DROP DATABASE ... SYNC`                      |
| sqlite                         | fresh file in `t.TempDir()`                                      |

Family names: postgresql, mysql, mariadb, tidb, cockroachdb, gaussdb, mssql,
oracle, clickhouse, sqlite.

## Environment variables

| variable                     | meaning |
|------------------------------|---------|
| `GORMGATE_ITEST_<KEY>_DSN`   | admin DSN override; KEY is the vendor key upper-cased, `-` → `_` (e.g. `GORMGATE_ITEST_SQLITE_PUREGO_DSN`). The admin user must be able to create/drop databases (Oracle: users). |
| `GORMGATE_ITEST_REQUIRE`     | if set, an unreachable server fails the test instead of skipping it (`make smoke` / `oracle-smoke` set it). |
| `GORMGATE_ITEST_SQL_LOG`     | if set, gorm logs every statement via `t.Log`. |

Default DSNs (see `internal/dbtest/vendors.go`):

```
pg14/pg18   postgres://gormgate:gormgate@127.0.0.1:15414/postgres?sslmode=disable&connect_timeout=5
mysql80     root:gormgate@tcp(127.0.0.1:13380)/?parseTime=true&timeout=5s     (same form for mysql84, mariadb*, tidb)
cockroach   postgres://root@127.0.0.1:15426/defaultdb?sslmode=disable&connect_timeout=5
mssql       sqlserver://sa:GormGate-Pass123@127.0.0.1:14330?database=master&dial+timeout=5
oracle      user="gormgate" password="gormgate" connectString="127.0.0.1:15210/FREEPDB1"
gauss       gaussdb://gormgate:Gormgate-123@127.0.0.1:15432/postgres?sslmode=disable&connect_timeout=5
clickhouse  clickhouse://gormgate:gormgate@127.0.0.1:19000/default?dial_timeout=5s
sqlite      file:{path}?_foreign_keys=1&_busy_timeout=5000
sqlite-purego file:{path}?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)
```

PostgreSQL-family, MSSQL and ClickHouse DSNs must be in URL form (the helper
rewrites the database part). SQLite DSNs are templates; `{path}` is replaced
with the per-test file.

## Oracle

gorm-oracle uses godror, which `dlopen`s Oracle Instant Client
(`libclntsh.so`) at runtime. The host has none, so on the host the Oracle test
fails at connect with `DPI-1047: Cannot locate a 64-bit Oracle Client library`
and is skipped (or fails with `GORMGATE_ITEST_REQUIRE`).

`oracle.Dockerfile` builds `gormgate-itest-oracle-runner` = `golang:1.26-bookworm`
+ Instant Client Basic Light 23.9.0.25.07 (`libaio1`, `ldconfig`). The
`oracle-runner` compose service (profile `runner`) bind-mounts the repository
root at `/src`, works in `/src/itest`, caches modules/builds in named volumes,
and reaches the server as `oracle:1521/FREEPDB1` on the compose network:

```sh
make -C itest oracle-smoke
# arbitrary tests:
make -C itest oracle-smoke ORACLE_RUN='TestSomething/^oracle$'
docker compose -f itest/compose.yaml --profile runner run --rm oracle-runner \
    go test -tags integration -count=1 -v -run 'TestSmoke/^oracle$' ./...
```

`docker/oracle/01-gormgate-admin.sql` runs once at container init and grants
the app user `gormgate` CREATE/ALTER/DROP USER, the per-schema create
privileges and UNLIMITED TABLESPACE WITH ADMIN OPTION, SELECT on `v_$session`
and ALTER SYSTEM (to kill leftover sessions before `DROP USER`).

## Verified per-vendor behaviour

DDL rollback = `CREATE TABLE` inside `BEGIN ... ROLLBACK`, then check whether
the table exists (asserted by `TestSmoke`):

| key                       | DDL rolled back? |
|---------------------------|------------------|
| pg14, pg18                | yes |
| gauss                     | yes |
| mssql                     | yes |
| sqlite, sqlite-purego     | yes |
| cockroach (defaults)      | **no** – see below |
| mysql80, mysql84          | no (implicit commit) |
| mariadb106, mariadb118    | no (implicit commit) |
| tidb                      | no |
| oracle                    | no (implicit commit) |
| clickhouse                | no (the "transaction" of clickhouse-go does not cover DDL) |

Quirks found while building this:

- **CockroachDB 25.2**: session variable `autocommit_before_ddl` defaults to
  `on`, so a DDL statement inside an explicit transaction commits the
  transaction first and the ROLLBACK does not undo it. With
  `SET autocommit_before_ddl = false` the same CREATE TABLE is rolled back
  (also asserted by `TestSmoke/cockroach`).
- **TiDB 8.5.1**: `tidb_enable_check_constraint` is `0` by default and CHECK
  constraints are parsed but not enforced (verified on a stock container).
  The compose service passes `--initialize-sql-file` with
  `docker/tidb/init.sql` (`SET GLOBAL tidb_enable_check_constraint = ON`);
  the file runs on first bootstrap, i.e. on every `up` because the store is
  on tmpfs. `TestSmoke/tidb` asserts the variable and the enforcement.
- **openGauss 7.0.0-RC3**:
  - Authentication: the image's entrypoint writes
    `password_encryption_type = 0` (md5 only) and `host all all 0.0.0.0/0 md5`.
    The gaussdb driver (gaussdb-go) authenticates fine with that as well, but
    compose switches to openGauss' native sha256: `OTHER_PG_CONF` appends
    `password_encryption_type = 2` (later line wins) before the users are
    created, and `GS_HOST_AUTH_METHOD=sha256` sets the hba method.
    `TestSmoke/gauss` asserts the user's stored hash is sha256, i.e. the driver
    passed sha256 authentication.
  - Databases default to `DBCOMPATIBILITY 'A'` (Oracle-like, e.g. `''` is
    NULL); dbtest creates every test database with `DBCOMPATIBILITY 'PG'`
    (asserted via `pg_database.datcompatibility`).
  - `DROP DATABASE ... WITH (FORCE)` is a syntax error; dbtest terminates the
    database's backends and retries the drop.
  - The container runs fine without `privileged`.
  - The tmpfs data dir must be owned by `omm` (uid 70).
- **SQL Server 2022** runs as uid 10001; its tmpfs (`/var/opt/mssql`) must be
  owned by that uid, otherwise it exits with
  `The system directory [/.system] could not be created ... Permission denied`.
- **Oracle Free** image `23-slim` currently reports
  `Oracle AI Database 26ai Free Release 23.26.3.0.0`.
- **CHECK constraints** are enforced on every vendor, including ClickHouse
  (`CONSTRAINT c CHECK expr`, checked on INSERT).
- Server versions seen: PostgreSQL 14.24 / 18.6, MySQL 8.0.46 / 8.4.11,
  MariaDB 10.6.28 / 11.8.9, TiDB 8.0.11-TiDB-v8.5.1, CockroachDB v25.2.23,
  SQL Server 2022 RTM-CU27 16.0.4295.3, openGauss 7.0.0-RC3,
  ClickHouse 25.8.33.6, SQLite 3.45.1 (mattn) / 3.41.2 (glebarez).
