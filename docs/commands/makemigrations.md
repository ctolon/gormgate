# makemigrations

Compares the models declared in your project with the state the existing
migrations describe, and writes Go files for the difference.

It never touches the database. The only thing it needs is your model
structs and the migrations you already have, which means you can run it
offline and review its output before anything is applied.

## Examples

Create the initial migrations. Name the apps the first time: like Django,
the bare form only looks at apps that already have a migrations package, so
on a fresh project it finds nothing to do.

```console
$ go run ./cmd/gorm-gate makemigrations
no changes detected

$ go run ./cmd/gorm-gate makemigrations auth blog
created auth/migrations/0001_initial.go
  + create model User
created blog/migrations/0001_initial.go
  + create model Post
  + create model Comment
```

After that the bare form picks up every change:

```console
$ go run ./cmd/gorm-gate makemigrations
created blog/migrations/0002_post_views.go
  + add field views to post
```

See what would be written, without writing it:

```console
$ go run ./cmd/gorm-gate makemigrations --dry-run
```

With `-v 3` the full file body is printed too, which is the quickest way to
check a generated migration before it lands.

Create an empty migration to hand-write a data migration into:

```console
$ go run ./cmd/gorm-gate makemigrations blog --empty --name backfill_slugs
```

Fail instead of writing, for CI:

```console
$ go run ./cmd/gorm-gate makemigrations --check --dry-run
```

`--check` exits non-zero when changes are missing, so a pull request that
edits a model without generating its migration fails the build.

## What it writes

The first migration of an app brings two more files with it:

- `<app>/migrations/migrations.go`, which registers the package, and
- `cmd/gorm-gate/zz_gormgate_migrations.go`, which blank-imports every
  migrations package.

That second file is why the next `go run ./cmd/gorm-gate` already knows
about the migration you just generated: migrations are compiled into your
command, not discovered at run time. Nothing has to be wired by hand.

## Renames

When a field or a model disappears and a similar one appears, the command
asks whether it was a rename, exactly as Django does:

```console
Did you rename post.body to post.content (a TextField)? [y/N]
```

Answer `N` and it generates a remove plus an add, which drops the column and
its data. With `--no-input` the question is never asked and the safe answer
is assumed.

<!-- BEGIN help -->

```console
$ gorm-gate help makemigrations
Compare the model structs with the migrations already written and
write new migrations for whatever differs.

Usage:
  gorm-gate makemigrations [app ...] [flags]

Flags:
      --check         exit non-zero if a migration is missing, and write nothing
      --dry-run       show what would be written, and write nothing
      --empty         write an empty migration to fill in by hand
  -h, --help          help for makemigrations
      --merge         write a migration that merges conflicting leaves
  -n, --name string   name for the new migration
      --no-header     omit the generated-by comment at the top of the file
  -y, --no-input      do not prompt; assume the answer that carries on
      --scriptable    write only the generated paths on stdout, everything else on stderr
      --update        fold the changes into the latest migration instead of writing a new one

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

- A migration name must be a valid Go identifier, because the file is Go
  source. `--name` is checked against that.
- `--update` folds the new changes into the app's latest migration instead
  of writing a new one. It refuses if that migration is already applied,
  is a squashed migration, or is depended on by another app.
- `--merge` resolves a conflict created by two branches adding migrations to
  the same app; it writes a merge migration that depends on both leaves.
