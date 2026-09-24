# Examples

Five self-contained projects. Each is its own Go module with a `replace`
pointing at the checkout above it, so `go build ./...` inside one builds
against the gormgate you have.

| | Read it for |
| --- | --- |
| [blogproj](blogproj/) | The smallest complete project: two apps, PostgreSQL, `cmd/gorm-gate`, and the first `makemigrations`. Start here. |
| [dockerproj](dockerproj/) | Running migrations as a deploy step before the app starts, with `compose.yaml` and a `Dockerfile`. |
| [datamigration](datamigration/) | Adding a `NOT NULL` column to a table with rows in it: `AddField`, a `RunGo` backfill over historical models, then `AlterField`. |
| [multidb](multidb/) | Two databases, a `Router` that keeps one app on each, `--database`, and what `showmigrations` reports on each. |
| [adoptdb](adoptdb/) | Taking over a database gormgate did not create: `inspectdb` for the models, `migrate --fake-initial` to adopt it. |

`datamigration`, `multidb` and `adoptdb` use SQLite files and need nothing
installed. `blogproj` needs a PostgreSQL database; `dockerproj` brings its
own with Docker Compose.
