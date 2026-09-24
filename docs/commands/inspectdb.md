# inspectdb

Reads an existing database and prints Go structs with gorm tags for its
tables.

It is the way into gormgate for a database you did not create: point it at
the schema, save what it prints, and you have models that describe what is
already there.

```console
$ go run ./cmd/gorm-gate inspectdb > models.go
$ go run ./cmd/gorm-gate inspectdb blog_post blog_comment
```

The output compiles as it stands. Review it before using it: column types
map onto Go types by the rules of the driver, and a type the backend does
not recognise is emitted with a comment saying so.

<!-- BEGIN help -->

```console
$ gorm-gate help inspectdb
Introspect a database and write model structs matching what is there,
as a starting point for adopting an existing schema.

Usage:
  gorm-gate inspectdb [table ...] [flags]

Flags:
      --database string      the database to work on (default "default")
  -h, --help                 help for inspectdb
      --include-partitions   also write models for partition tables
      --include-views        also write models for views

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

- After saving the structs, `migrate --fake-initial` lets you adopt the
  existing tables without recreating them.
- `--include-views` adds database views, and `--include-partitions` adds
  partitions; the latter is PostgreSQL-only, as in Django.
- Table and column names that are not valid Go identifiers get a
  `gorm:"column:..."` tag, so the struct field can be named normally.
