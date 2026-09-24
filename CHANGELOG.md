# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-09-24

First release.

### Added

- Django 6.0's migration framework, ported to Go and GORM: the project
  state, the migration graph, the loader, the autodetector, the optimizer,
  the executor, the squash machinery and the schema editors. Given the same
  models, gormgate writes the same migrations, with the same operations, in
  the same order.
- The management commands, with Django's names, positional arguments and
  exit statuses: `makemigrations`, `migrate`, `sqlmigrate`, `showmigrations`,
  `squashmigrations`, `optimizemigration`, `sqlsequencereset`, `inspectdb`,
  `check`, `help`, `completion` and `version`. What they print is gormgate's
  own; `docs/from-django.md` maps the command line onto Django's.
- Migrations as ordinary Go files, registered from `init()` and compiled
  into the project's own `gorm-gate` command, with `RunGo` data migrations
  that see historical models built from the migration state rather than
  from today's structs.
- Database backends for PostgreSQL, CockroachDB, GaussDB/openGauss, SQLite,
  MySQL, MariaDB, TiDB, SQL Server, Oracle and ClickHouse, each verified
  against a real server. See [docs/vendors.md](docs/vendors.md).
- `gormgate.Command` returns the command tree as a `*cobra.Command`, so a
  project can mount gorm-gate inside its own CLI instead of handing the
  whole program over to `Execute`.
- A `compatibility` system check tag, reported through `check`, that says
  what will not work *before* `migrate` finds out: a `Settings.NamingStrategy`
  that disagrees with the connection's (`gormgate.E002`), objects the
  backend refuses outright (`gormgate.E003`), objects it skips silently
  (`gormgate.W001`), and a model a backend will migrate but never let the
  project change again (`gormgate.W002`). Naming the tag also prints an
  inventory of what gormgate owns in the project (`gormgate.I001`).
- `migrations.SQL`, a sealed sum type — `Script`, `Statements`,
  `Parameterized` — so a `RunSQL` written with the wrong shape is a compile
  error rather than a failure part way through a migration.
- Named types for the enumerated field values: `SortOrder`, `Deferrable`
  and `ReferentialAction`, so a typo is a compile error.
- `migrations.ForeignKeyConstraint`, a table-level foreign key over one or
  more columns, which is what gorm builds from
  `foreignKey:A,B;references:X,Y`. Every backend supports it except
  ClickHouse, which has no foreign keys at all.
- Unmanaged models may share a table with a managed one, which is how a
  database view — or the nearest thing to a Django proxy model — is mapped.
- `migrations/postgres`, the port of `django.contrib.postgres.operations`:
  `CreateExtension` and the named extension shortcuts,
  `AddIndexConcurrently`, `RemoveIndexConcurrently`, `CreateCollation`,
  `RemoveCollation`, `AddConstraintNotValid` and `ValidateConstraint`. Each
  asks the schema editor what the server has rather than matching a vendor
  name, so a new PostgreSQL-family backend needs no change here.
- `migrations.Hints`, the typed record a database router is given: the
  model, its name, and whatever `Hints` map the operation carried.
- `migrations.IndexClass` and `migrations.IndexMethod`, so an index's class
  and access method are named types rather than bare strings.
- `backends/base.Grammar`, how a backend spells each statement: one method
  per statement kind, each taking a struct of already-quoted parts. A
  backend embeds `BaseGrammar` and writes only what it spells differently,
  and a part the caller has nothing for is an empty field rather than a
  missing key, so there is no rendering that can fail at run time.
- A parity harness that runs the same command sequences against a real
  Django 6.0 project and an equivalent gormgate project and requires the two
  to agree on what they did — the same exit status, the same migrations
  written, the same operations planned — and a per-database conformance
  suite that applies every operation forwards and backwards and compares the
  resulting schema with one built from scratch.

[Unreleased]: https://github.com/ctolon/gormgate/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ctolon/gormgate/releases/tag/v0.1.0
