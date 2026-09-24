# migrate

Applies the migrations that have not been applied yet, or unapplies back to
a target you name.

## Examples

Bring the database up to date:

```console
$ go run ./cmd/gorm-gate migrate
applying auth.0001_initial ... ok
applying blog.0001_initial ... ok
```

Migrate one app to a specific migration, forwards or backwards:

```console
$ go run ./cmd/gorm-gate migrate blog 0002
```

Unapply everything in an app — this drops its tables:

```console
$ go run ./cmd/gorm-gate migrate blog zero
```

Show the plan without running it:

```console
$ go run ./cmd/gorm-gate migrate --plan
```

## Adopting an existing database

If the tables already exist because they were created by
`gorm.AutoMigrate`, `--fake-initial` marks the *initial* migration of an app
as applied when its tables are already there, instead of trying to create
them again:

```console
$ go run ./cmd/gorm-gate migrate --fake-initial
```

This works because gormgate emits the same DDL gorm does for the same
models. Note the word *initial*: later migrations are applied normally.

`--fake` is the blunt version — it records migrations as applied without
running any SQL, for when you have already made the change by hand.

<!-- BEGIN help -->

```console
$ gorm-gate help migrate
Apply the migrations that have not been applied yet. Naming an app
limits the run to it; naming a migration moves that app to exactly
that point, forwards or backwards. The name zero unapplies them all.

Usage:
  gorm-gate migrate [app] [migration] [flags]

Flags:
      --check             exit non-zero if anything is unapplied, and change nothing
      --database string   the database to work on (default "default")
      --fake              record the migrations as applied without running them
      --fake-initial      record an initial migration as applied when its tables are already there
  -h, --help              help for migrate
  -y, --no-input          do not prompt; assume the answer that carries on
      --plan              list what would be applied, and stop
      --prune             forget recorded migrations whose files are gone
      --run-syncdb        create the tables of apps that have no migrations

Global Flags:
      --force-color       colorize the output even when it is not a terminal
      --no-color          do not colorize the output
  -q, --quiet             only say what must be said
      --settings string   settings to use, as registered with gormgate.ExecuteSet
      --skip-checks       do not run the system checks first
  -v, --verbose count     more detail; repeat for more still
```

<!-- END help -->

## Notes

- `--check` exits non-zero if any migration is unapplied and changes
  nothing. It is the deployment-gate counterpart of
  `makemigrations --check`.
- `--run-syncdb` creates the tables of apps that have no migrations at all.
- `--prune` deletes recorded migrations whose files no longer exist, which
  happens after a squash. It requires an app label.
- Whether a migration runs inside a transaction depends on the database and
  on the migration's `Atomic` field; see
  [Databases](../vendors.md) for which backends can roll back DDL.
- `--database` selects one of the databases in `Settings.Databases`.
