# gormgate

Django-style migrations for [GORM](https://gorm.io).

gormgate is a port of Django 6.0's migration framework: the autodetector,
the migration graph, the optimizer, the executor, the squash machinery and
the management commands. Given the same models it writes the same
migrations, with the same operations, in the same order, and returns the
same exit codes. Migrations are Go files, generated and compiled into your
own `gorm-gate` command.

```console
$ go run ./cmd/gorm-gate makemigrations blog
created blog/migrations/0002_post_views.go
  + add field views to post

$ go run ./cmd/gorm-gate migrate
applying blog.0002_post_views ... ok
```

## Why

`AutoMigrate` is good at making a database look like your structs. It is not
a migration system: it will not drop a column, rename one, tell you what it
is about to do, let you review the SQL, run a data migration between two
schema changes, or reproduce the same sequence of steps on every
environment.

gormgate adds the part that is missing, and does it by porting a design that
has been carrying production databases since Django 1.7 in 2014, rather
than inventing one.

## What you get

- **Migrations you can read and edit.** A migration is a Go file with a list
  of operations. It is generated for you, and it is yours to change.
- **A real autodetector.** It notices added, removed, renamed and altered
  fields, models, indexes and constraints and works out the dependencies
  between apps. What it cannot decide alone — whether a field was renamed
  or dropped and re-added, what to put in a new `NOT NULL` column — it
  asks.
- **Ten databases**, each verified against a real server: PostgreSQL,
  CockroachDB, GaussDB/openGauss, SQLite, MySQL, MariaDB, TiDB, SQL Server,
  Oracle and ClickHouse. See [Databases](vendors.md).
- **The SQL, before you run it.** `sqlmigrate` prints exactly what a
  migration will execute.
- **Schemas identical to `AutoMigrate`'s.** gormgate emits the same DDL gorm
  does for the same models, so you can adopt an existing database with
  `migrate --fake-initial`.

## How it fits together

A project declares its apps and databases in its own command:

```go
func main() {
	gormgate.Execute(&gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("auth", &auth.User{}),
			gormgate.App("blog", &blog.Post{}, &blog.Comment{}),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
				return gorm.Open(postgres.Open(os.Getenv("DSN")))
			}},
		},
	})
}
```

`makemigrations` writes `blog/migrations/0001_initial.go`, a `migrations.go`
that registers the package, and `cmd/gorm-gate/zz_gormgate_migrations.go`
which imports every migrations package — so the next `go run
./cmd/gorm-gate` already has the new migration compiled in. Nothing has to
be wired by hand.

[Get started :material-arrow-right:](getting-started.md){ .md-button .md-button--primary }

## Relationship to Django

The commands, their arguments and their exit codes are Django 6.0's, and so
are the decisions they take: a parity harness runs the same sequences
against a real Django project and an equivalent gormgate project and
requires the two to write the same migrations and plan the same operations.

What the commands print is gormgate's own, and a few options are spelled
the way Go command line tools spell them. [Feature map](from-django.md) maps
the command line and the model layer onto Django's; Django concepts that
have no GORM equivalent are in [Not applicable](not-applicable.md).
