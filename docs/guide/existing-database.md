# An existing database

gormgate is designed to be adopted, not only started with. Two things make
that work: `inspectdb`, which turns a schema into models, and
`--fake-initial`, which accepts tables that are already there.

## If you already use AutoMigrate

This is the easy case. gormgate emits the same DDL gorm does for the same
models — a per-database parity suite enforces it — so your existing tables
already match what the initial migration would create.

```console
$ go run ./cmd/gorm-gate makemigrations auth blog
$ go run ./cmd/gorm-gate migrate --fake-initial
applying auth.0001_initial ... faked
applying blog.0001_initial ... faked
```

`faked` means: the tables were found, so the migration was recorded as
applied without running its SQL. From here on, stop calling `AutoMigrate`
and let migrations do the work.

Check first that the two really do agree:

```console
$ go run ./cmd/gorm-gate sqlmigrate blog 0001
```

Compare that with your tables. If something differs — a column gorm would
not create, an index added by hand — either bring the model in line or edit
the initial migration to describe what is actually there.

!!! note "Only the initial migration is faked"
    `--fake-initial` applies to migrations marked `Initial` whose tables
    already exist. Everything after them is applied normally.

## If the database came from somewhere else

Start from the schema:

```console
$ go run ./cmd/gorm-gate inspectdb > models.go
```

You get gorm structs with tags, one per table. Read them before using them:
column types map onto Go types by the driver's rules, and a type the backend
does not recognise is emitted with a comment saying so. Split them into apps
however you like, then generate and fake-apply the initial migrations as
above.

`inspectdb` can also take table names, and `--include-views` /
`--include-partitions` (PostgreSQL) for those.

## Tables gormgate should not touch

Some tables belong to another system. Declare the model with
`Managed: false` and gormgate will keep it in its state — so foreign keys to
it resolve — but never create, alter or drop it:

```go
--8<-- "doc_snippets_test.go:unmanaged"
```

## Apps with no migrations at all

An app you have not generated migrations for is *unmigrated*. `migrate
--run-syncdb` creates its tables directly, the way `AutoMigrate` would:

```console
$ go run ./cmd/gorm-gate migrate --run-syncdb
synchronizing apps without migrations
creating tables
    creating table legacy_audit
  running deferred SQL
applying blog.0002_post_views ... ok
```

This is a way to keep a peripheral app out of the migration history, not a
substitute for migrating it.
