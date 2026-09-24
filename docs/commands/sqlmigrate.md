# sqlmigrate

Prints the SQL a migration would run, without running it.

```console
$ go run ./cmd/gorm-gate sqlmigrate blog 0002
BEGIN;
--
-- add field views to post
--
ALTER TABLE "blog_posts" ADD COLUMN "views" bigint DEFAULT 0 NOT NULL;
COMMIT;
```

This is the command to reach for when a DBA has to review a change before it
is applied, or when you want to apply it by hand.

`--backwards` prints the SQL for unapplying it instead.

<!-- BEGIN help -->

```console
$ gorm-gate help sqlmigrate
Print the statements a migration would run against a database. The
migration is not applied and nothing is recorded.

Usage:
  gorm-gate sqlmigrate app migration [flags]

Flags:
      --backwards         print the SQL that unapplies the migration instead
      --database string   the database to work on (default "default")
  -h, --help              help for sqlmigrate

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

- The script is wrapped in `BEGIN`/`COMMIT` only on databases that can roll
  back DDL. On MySQL, MariaDB, TiDB, CockroachDB, Oracle and ClickHouse it
  is not, because there every statement commits as it runs.
- On SQLite the script is bracketed with `PRAGMA foreign_keys = OFF/ON`,
  because a collected script is replayed on a connection this command
  cannot configure.
- A `RunGo` operation has no SQL. Its comment appears in the output, with
  nothing under it.
- The SQL is generated against the *current* database's dialect, so run it
  with `--database` pointed at the same kind of server you will apply it to.
