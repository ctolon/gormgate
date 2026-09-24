# showmigrations

Lists the migrations of the project and which of them are applied.

```console
$ go run ./cmd/gorm-gate showmigrations
auth
 [X] 0001_initial
blog
 [X] 0001_initial
 [ ] 0002_post_views
```

(`0002_post_views` is written but not yet applied.)

`[X]` means recorded as applied. With `-v 2` the time it was applied is
shown next to it.

The other format is the plan, which shows the order the executor would use
and the dependencies that force it:

```console
$ go run ./cmd/gorm-gate showmigrations --plan
[X]  auth.0001_initial
[X]  blog.0001_initial
[ ]  blog.0002_post_views
```

<!-- BEGIN help -->

```console
$ gorm-gate help showmigrations
List each app's migrations and mark the ones applied to a database.
With --plan, list them in the order they would run instead.

Usage:
  gorm-gate showmigrations [app ...] [flags]

Flags:
      --database string   the database to work on (default "default")
  -h, --help              help for showmigrations
  -p, --plan              list the migrations in the order they would run, with their dependencies

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

- `--list` is the default; `--plan` replaces it.
- A migration whose file is gone but whose record remains is shown too —
  that is what `migrate --prune` cleans up.
