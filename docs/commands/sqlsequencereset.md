# sqlsequencereset

Prints the SQL that resets each table's primary-key sequence past the
largest key currently stored.

You need this after loading rows with explicit primary keys — a dump, a
fixture, a migration that inserted known ids — because the sequence does not
know about them and the next insert would collide.

```console
$ go run ./cmd/gorm-gate sqlsequencereset blog
BEGIN;
SELECT setval(pg_get_serial_sequence('"blog_comments"','id'), coalesce(max("id"), 1), max("id") IS NOT null) FROM "blog_comments";
SELECT setval(pg_get_serial_sequence('"blog_posts"','id'), coalesce(max("id"), 1), max("id") IS NOT null) FROM "blog_posts";
COMMIT;
```

The command only prints; pipe it into your client to run it.

<!-- BEGIN help -->

```console
$ gorm-gate help sqlsequencereset
Print the SQL statements that reset the primary key sequences of
every model in the given apps, for a database that has sequences.

Usage:
  gorm-gate sqlsequencereset app [app ...] [flags]

Flags:
      --database string   the database to work on (default "default")
  -h, --help              help for sqlsequencereset

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

- Nothing is printed for databases that have no sequences to reset: MySQL,
  MariaDB, TiDB, SQL Server, CockroachDB (whose keys come from
  `unique_rowid()`) and ClickHouse. Django's backends behave the same way.
- On Oracle the reset is a PL/SQL block that drops and recreates the
  identity sequence.
