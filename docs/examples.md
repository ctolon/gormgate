# Examples

Five self-contained projects live under
[`examples/`](https://github.com/ctolon/gormgate/tree/main/examples) in the
repository. Each is its own Go module with a `replace` pointing at the
checkout above it, so `go build ./...` inside one builds against the
gormgate you have, and each README's console output was captured from a
real run.

| | Read it for |
| --- | --- |
| [blogproj](https://github.com/ctolon/gormgate/tree/main/examples/blogproj) | The smallest complete project: two apps, PostgreSQL, `cmd/gorm-gate`, and the first `makemigrations`. Start here. |
| [dockerproj](https://github.com/ctolon/gormgate/tree/main/examples/dockerproj) | Running migrations as a deploy step before the app starts, with `compose.yaml` and a `Dockerfile`. |
| [datamigration](https://github.com/ctolon/gormgate/tree/main/examples/datamigration) | Adding a `NOT NULL` column to a table that already has rows: `AddField`, a `RunGo` backfill over historical models, then `AlterField`. |
| [multidb](https://github.com/ctolon/gormgate/tree/main/examples/multidb) | Two databases and a `Router` that keeps one app on each, `--database`, and what `showmigrations` reports on each. |
| [adoptdb](https://github.com/ctolon/gormgate/tree/main/examples/adoptdb) | Taking over a database gormgate did not create: `inspectdb` for the models, `migrate --fake-initial` to adopt it. |

`datamigration`, `multidb` and `adoptdb` use SQLite files and need nothing
installed. `blogproj` wants a PostgreSQL database; `dockerproj` brings its
own with Docker Compose.

## The one they all show

Migrations are compiled into your command, so the loop is always the same
three steps:

```console
$ go run ./cmd/gorm-gate makemigrations <app>   # write the migration
$ go run ./cmd/gorm-gate sqlmigrate <app> 0002  # read what it will do
$ go run ./cmd/gorm-gate migrate                # do it
```

`dockerproj` is the one to copy if you are deploying: it separates the
migration from the application start, which is what makes a rollback
unambiguous and stops every replica from migrating at once.
