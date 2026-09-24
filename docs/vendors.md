# Databases

Every backend is verified against a real server (see `itest/`): the
operation conformance suite (`TestConformance`) applies each operation
forwards, compares the resulting schema with one built from scratch from
the final state, unapplies it, re-applies it, replays the SQL that
`sqlmigrate` collects and checks that existing rows survive; the gorm
parity suite (`TestGormParity`) requires the schema gormgate creates to be
identical to the one `AutoMigrate` creates for the same models, and
requires `AutoMigrate` over a gormgate-created schema to want exactly what
it wants over its own.

The per-vendor facts below are the ones the tests established on the
pinned server versions in `itest/compose.yaml`; the feature flags are in
each backend's `Features` value, using Django's names.

## PostgreSQL (`backends/postgresql`, tested on 14 and 18)

- DDL is transactional: a failing migration leaves the schema untouched.
- `serial`/`bigserial` columns (what gorm emits for an auto-incrementing
  key) are handled explicitly when a column's type changes: the sequence is
  created, retyped or dropped as needed, and `GENERATED … AS IDENTITY`
  columns (gorm's `generated:identity` tag) are added and dropped with
  `ALTER COLUMN … ADD/DROP IDENTITY`.
- Type changes add a `USING column::type` cast when the base type changes,
  as Django does.
- Comments are set with `COMMENT ON TABLE/COLUMN`; partial, expression and
  covering indexes, deferrable unique constraints and `NULLS NOT DISTINCT`
  are supported.
- Renaming a table or a column keeps the names PostgreSQL derived for the
  primary key constraint and the sequence, exactly as in Django; the
  conformance suite normalizes those database-generated names.
- gormgate does not create the `varchar_pattern_ops` "_like" indexes Django
  creates, because gorm doesn't (see `docs/deviations.md`).

## CockroachDB (`backends/cockroachdb`, tested on v25.2, minimum 24.1)

Built on the PostgreSQL backend (`postgresql.Editor` and
`postgresql.Introspection` are embedded); the differences below are the
whole of it. The detector takes precedence over PostgreSQL on the shared
`postgres` dialector and identifies the server by its version string.

- DDL commits implicitly (`autocommit_before_ddl` is on by default in
  v25.2), so no migration transaction is opened and
  `atomic_failure_rolls_back` skips.
- Auto-incrementing keys default to `unique_rowid()`, not a sequence: the
  values are unordered, `sqlsequencereset` prints nothing, and a type change
  sets or drops that default instead of PostgreSQL's sequence handling. The
  one conformance case that assumes keys start at 1 skips on
  `NonSequentialAutoIncrement`.
- A primary key cannot be dropped on its own (`crdb#48026`), so `CreatePK`
  and the primary-key path of an alteration use
  `ALTER TABLE ... ALTER PRIMARY KEY USING COLUMNS (...)`, and the secondary
  unique index CockroachDB leaves behind (`<table>_<cols>_key`) is dropped
  afterwards — without that the migrated schema would never equal a
  from-scratch one. Dropping a primary key with no replacement to install
  returns an error naming the upstream issue.
- Index names are table-scoped: `DROP INDEX <table>@<name>` and
  `ALTER INDEX <table>@<old> RENAME TO <new>`. A unique constraint is
  dropped with `DROP INDEX ... CASCADE` (`crdb#42840`).
- No deferrable unique constraints (`crdb#31632`), no `NULLS NOT DISTINCT`,
  and sub-commands are never combined into one `ALTER TABLE` (`crdb#49351`).
- Comments are supported and used, unlike django-cockroachdb, which only
  disables them for speed; gorm's `AutoMigrate` writes comments, so parity
  requires them.
- Introspection excludes `pg_extension`, `crdb_internal` and
  `information_schema`, where CockroachDB keeps PostGIS-compatibility tables
  that PostgreSQL's query would report as user tables, and reads index
  column ordering from the `prefix` access method, CockroachDB's only
  ordered one.

## GaussDB / openGauss (`backends/gaussdb`, tested on openGauss 7.0)

Also built on the PostgreSQL backend, reached through
`gorm.io/driver/gaussdb`.

- The database must be `DBCOMPATIBILITY 'PG'`. In the other modes the empty
  string is NULL and nullability, defaults and introspection all behave
  differently, so the backend refuses to open such a database and says how
  to recreate it (`TestGaussDBRequiresPGCompatibility`).
- DDL is transactional. But an `ALTER TABLE` carrying several sub-commands
  is rejected on a column whose type was already altered earlier in the same
  transaction ("cannot alter type of column ... twice"), while the same
  sub-commands as separate statements are accepted — so sub-commands are
  never combined, which is also Django's default.
- `CREATE/ALTER SEQUENCE ... AS <type>` is PostgreSQL 10 syntax and does not
  exist here, so sequences are created untyped and a `serial` to `bigserial`
  change needs no sequence statement at all, exactly as on PostgreSQL 9.
- There are no identity columns (`GENERATED ... AS IDENTITY` is a syntax
  error); the backend says so rather than emitting DDL that cannot work.
  Generated columns exist, and modifying one returns Django's
  `Modifying GeneratedFields is not supported` message.
- No covering indexes (`INCLUDE` needs the ubtree access method) and no
  `NULLS NOT DISTINCT`.
- Introspection is rewritten where the catalogue differs: there is no
  `pg_class.relispartition`, no `pg_attribute.attidentity` (a `nextval(`
  default alone identifies an auto-increment column) and no multi-argument
  `unnest`, so the index query walks `indkey`/`indoption` with
  `generate_series`.

## SQLite (`backends/sqlite3`, tested with mattn 3.45 and modernc 3.41)

- DDL is transactional.
- SQLite has no `ALTER TABLE ... ALTER COLUMN`, so most changes rebuild the
  table: Django's `_remake_table` is ported (create `new__<table>` from the
  altered state, `INSERT ... SELECT` the data — with `coalesce(old,
  default)` when a column becomes NOT NULL — drop the old table, rename).
  Native `ADD COLUMN`, `DROP COLUMN` (SQLite ≥ 3.35.5) and `RENAME COLUMN`
  are used under exactly Django's conditions.
- `PRAGMA foreign_keys` cannot change inside a transaction, so the schema
  editor turns it off before the migration transaction and back on after
  it, and runs `PRAGMA foreign_key_check` before committing — a violation
  rolls the migration back, with Django's message.
- Foreign keys, unique constraints and checks are named table constraints
  written inside `CREATE TABLE`, as gorm writes them, so adding, altering
  or removing one rebuilds the table.
- No comments (SQLite has none), no covering or deferrable constraints, no
  `RENAME INDEX`, no sequence reset. Table names are compared
  case-insensitively.
- Constraint names are recovered by parsing the `CREATE TABLE` text stored
  in `sqlite_master`; unnamed constraints fall back to Django's
  `__unnamed_constraint_<n>__` spelling.
- `sqlmigrate` brackets the script with `PRAGMA foreign_keys = OFF/ON`,
  because a collected script is replayed on a connection the command can't
  configure.

## MySQL and MariaDB (`backends/mysql`, tested on MySQL 8.0/8.4 and MariaDB 10.6/11.8)

- DDL commits implicitly: a failing migration leaves the statements that
  already ran in place, so gormgate never opens a migration transaction
  (`can_rollback_ddl = false`). `RunGo` marked atomic still runs in a real
  transaction.
- Column changes use `MODIFY`, which replaces the whole definition, so the
  nullability, the default and the comment are always restated — Django
  restates the default but not the comment, which loses it when both
  change at once.
- Dropping a foreign key also drops the index MySQL created for it, so the
  result matches a table created from scratch without the key. The two
  statements are separate because TiDB rejects the combined form.
- Comments are written inline in the column definition, like gorm.
- No partial indexes, no covering or deferrable constraints. Expression
  indexes need MySQL 8.0.13+ and are unavailable on MariaDB. Index column
  ordering needs InnoDB (MySQL) or MariaDB 11.8.
- MariaDB 10.6 reports the default of a fresh nullable column as the
  string "NULL" and as SQL NULL after `DROP DEFAULT`; both are normalized
  to "no default".
- `sqlsequencereset` prints nothing: Django's MySQL backend has no
  `sequence_reset_sql` either.
- Minimum versions, as in Django: MySQL 8.0.11, MariaDB 10.6.

## TiDB (`backends/tidb`, tested on v8.5.1)

- Behaves like MySQL, with `can_rollback_ddl = false`, and needs `ADD
  COLUMN` and `ADD FOREIGN KEY` in separate statements (which is what
  gormgate always does). Foreign keys need TiDB 6.6+.
- A clustered integer primary key cannot change type, lose or gain
  `AUTO_INCREMENT`; gormgate raises `NotSupportedError` naming the table,
  the column, both types and the server's message before running
  anything, and the three affected conformance cases skip on the
  `CannotAlterIntegerPrimaryKeyType` feature flag.
- `DESC` in an index is accepted but stored ascending, so index column
  ordering is reported as unsupported.
- Check constraints live in `TIDB_CHECK_CONSTRAINTS` rather than
  `information_schema.TABLE_CONSTRAINTS`. gorm reads the latter, so gorm's
  own `AutoMigrate` is not idempotent on TiDB for models with check
  constraints; the gorm-parity suite pins gormgate to the same behaviour
  gorm has on its own schema.
- Table names are compared case-insensitively
  (`lower_case_table_names = 2`).

## SQL Server (`backends/mssql`, tested on 2022)

- DDL is transactional (verified: a `CREATE TABLE` inside a rolled-back
  transaction leaves nothing behind).
- Tables, columns and indexes are renamed with `sp_rename`; `ALTER COLUMN`
  carries the full type and repeats the current nullability.
- Defaults are named DEFAULT constraints. Inside `CREATE TABLE` they are
  left unnamed so that SQL Server names them exactly as it does for gorm;
  on the ALTER path gormgate gives them a deterministic name and drops
  them with `DROP CONSTRAINT IF EXISTS`, so the SQL `sqlmigrate` prints can
  be replayed.
- Everything that depends on a column — indexes, unique constraints, the
  primary key, check constraints, the default constraint and incoming
  foreign keys — is dropped before an `ALTER COLUMN` and recreated
  afterwards, because SQL Server refuses the change otherwise (`Msg 5074`,
  `Msg 4922`).
- The IDENTITY property cannot be added to or removed from an existing
  column (`Msg 156`); gormgate raises `NotSupportedError` and the two
  affected conformance cases skip on `CannotAlterAutoIncrement`.
- Unique constraints stay plain `UNIQUE` constraints, as gorm writes them,
  so a nullable unique column accepts only one NULL
  (`UniqueConstraintsRejectMultipleNulls`).
- Comments are stored as `MS_Description` extended properties. Filtered
  (partial) and covering indexes are supported; expression indexes are
  not. `sqlsequencereset` prints nothing, as in mssql-django.
- Server-generated names (`PK__…`, `DF__…`) are normalized by the
  conformance suite, which compares primary keys and default constraints
  by the columns they cover.

## Oracle (`backends/oracle`, tested on Oracle Free 23)

- Every DDL statement commits implicitly, so gormgate opens no migration
  transaction (`can_rollback_ddl = false`).
- Auto-incrementing keys are `GENERATED BY DEFAULT AS IDENTITY` columns, as
  gorm creates them, so `DeleteModel` drops no separate sequence (Django
  drops the `<TABLE>_SQ` sequence it creates itself).
- Empty strings are NULL (`interprets_empty_strings_as_nulls`), so string
  and byte columns are never written `NOT NULL`, exactly as in Django.
- Oracle refuses a type change on a non-empty column (`ORA-01439`), a
  precision decrease (`ORA-01440`), adding an identity to an existing
  column (`ORA-30673`) and any LOB conversion (`ORA-22858/22859`).
  Django's add/copy/drop/rename workaround is ported for all of them. The
  two data-independent cases (adding an identity, converting to or from a
  LOB) are *predicted* rather than only caught, because in the collect mode
  behind `sqlmigrate` no statement runs and no error can be caught.
  Unlike Django's, the temporary column carries no primary key, unique
  constraint, foreign key or comment, so the final rename recreates them
  under the definitive column name.
- `MODIFY ... NULL` on an already-nullable column raises `ORA-01451` (and
  `ORA-01442` the other way around), so nullability is only written when
  introspection shows it actually changes.
- Oracle foreign keys have no `ON UPDATE` action (`ORA-02000`);
  `NO ACTION`/`RESTRICT` are the server default and emit nothing. An
  `OnUpdate` in a gorm tag raises `NotSupportedError` naming the table, the
  column, the constraint and the trigger-based alternative
  (`NoForeignKeyOnUpdate`).
- Identifiers are quoted verbatim, as gorm-oracle's `QuoteTo` does, instead
  of being upper-cased and truncated to 30 characters like Django's; the
  name limit is therefore Oracle 12.2's 128, and introspection matches
  names exactly rather than case-insensitively.
- A `NOT NULL` is catalogued as a system-named check constraint, and a
  `DESC` or expression index element as a hidden virtual column; the
  conformance suite's Oracle normalizer resolves both, plus the
  per-database identity sequence name.
- No partial indexes (the two filtered-index cases skip on
  `SupportsPartialIndexes`). Comments, expression indexes, deferrable
  unique constraints, index column ordering and `RENAME INDEX` all work.
- `TestGormParity` runs a corpus without `type:text`, `check:` and
  `comment:`: gorm-oracle cannot create those at all (`ORA-00902`,
  `ORA-01741`, and no `COMMENT ON` is emitted). gormgate itself supports
  all three, and the conformance suite exercises them.

## ClickHouse (`backends/clickhouse`, tested on 25.8, minimum 22.8)

ClickHouse is supported as a documented subset. Everything outside it
raises `migrations.NotSupportedError` with the operation, the model, the
field and the reason.

- No transactions and no DDL rollback; a failed migration leaves the
  statements that already ran in place.
- No foreign keys -- on a field or as a table-level
  `ForeignKeyConstraint` -- no unique constraints, no `unique_together`, no
  unique or plain indexes, and no `RENAME INDEX`. Indexes are data-skipping
  indexes and therefore need an explicit type (`minmax`, `set(n)`,
  `bloom_filter`, …); `GRANULARITY` comes from the index option.
- Columns have no nullability and no `AUTO_INCREMENT`: both are accepted in
  the state and produce no DDL, exactly as gorm's ClickHouse migrator
  behaves (`NoColumnNullability`). Column types are byte-identical to
  `Dialector.DataTypeOf`.
- A column that belongs to the `ORDER BY`, `PRIMARY KEY` or `PARTITION BY`
  key cannot be altered, renamed or dropped, and a field's primary-key flag
  cannot change; all of these are rejected before anything runs.
- The default engine is `MergeTree()` with `ORDER BY` the primary key
  columns (`tuple()` without one), as gorm does. `Options.ClickHouse`
  overrides the engine, `PARTITION BY`, `PRIMARY KEY`, `ORDER BY` and
  `SETTINGS`.
- A one-off default (Django's `preserve_default=False`) is written as
  `ADD COLUMN ... DEFAULT`, `MATERIALIZE COLUMN`, then
  `MODIFY COLUMN ... REMOVE DEFAULT`; without the materialize step the old
  rows would read back the type's zero value.
- Mutations are made synchronous (`SETTINGS mutations_sync = 2`, also set
  on the connection) so that a `RunGo` operation can read back its own
  writes and so that collected `sqlmigrate` SQL replays deterministically;
  `DROP TABLE` uses `SYNC` for the same reason.
- The migrations table stores `app` and `name` as `String`, not as a sized
  string: gorm maps a sized string to `FixedString(n)`, which NUL-pads
  every value, and the recorder compares those values with the migration
  names on disk.
- Casts to sized strings are unimplemented in the server
  (`CAST AS FixedString is only implemented for types String and
  FixedString`), so the two conformance cases that widen or retype a sized
  string skip on `NoCastsToSizedStrings`.
- Comments (inline), expression data-skipping indexes and table check
  constraints are supported. `sqlsequencereset` prints nothing.
