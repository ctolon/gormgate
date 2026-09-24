# Leaving gormgate

gormgate reads plain gorm models. The only thing it ever asks a model for is
an optional `MigrationMeta()` method, and even that is a method on your own
struct in your own package. Nothing in your model layer is gormgate's, so
leaving it is a matter of deleting files — the models keep working, and so
does `db.AutoMigrate` if that is what you go back to.

## What gormgate owns

Ask the project itself rather than going from memory:

```console
$ go run ./cmd/gorm-gate check --tag compatibility
System check identified some issues:

INFOS:
?: (gormgate.I001) gormgate owns these, and nothing else, in this project:
	  auth/migrations (auth, 1 migration file)
	  blog/migrations (blog, 1 migration file)
	  cmd/gorm-gate/zz_gormgate_migrations.go
	  the 'gormgate_migrations' table of database 'default'
	HINT: Your model structs are plain gorm, so nothing about them has to change to stop using gormgate: delete the files listed above and drop the table, and they keep working.

System check identified 1 issue (0 silenced).
```

That is the whole list, and it is worth reading before you adopt gormgate as
well as before you drop it. In detail:

- **The migrations packages.** One directory per app, holding
  `migrations.go` and the numbered migration files. They import
  `github.com/ctolon/gormgate/migrations` and nothing else of yours.
- **`cmd/<command>/zz_gormgate_migrations.go`.** The generated file that
  blank-imports every migrations package so they are compiled into the
  command. `makemigrations` writes it; nothing else touches it.
- **The `gorm-gate` command itself**, wherever you put its `main` package
  and its `gormgate.Settings`.
- **The `gormgate_migrations` table**, one per database. It records which
  migration of which app has been applied, and nothing else: no schema
  information lives in it, so the tables it describes do not depend on it.
- **Your model structs: nothing.** No embedded type, no registration call,
  no build tag.

## Dropping it

1. Delete the migrations directories, `zz_gormgate_migrations.go` and the
   `cmd/gorm-gate` package.
2. Drop the recorder table in each database:

    ```sql
    DROP TABLE gormgate_migrations;
    ```

    Dropping it changes no other table. Keep it instead if you might come
    back: with the table and the migration files in place, `migrate` picks
    up exactly where it left off.
3. Remove the module requirement:

    ```console
    $ go mod tidy
    ```

Your schema stays exactly as the last `migrate` left it. What you lose is
the history and the ability to generate the next change from a model diff,
not any part of the database.

## Coming back, or moving on

If you are moving to another migration tool rather than to nothing,
`sqlmigrate` prints the SQL of any migration without touching the database,
which is the usual way to seed the new tool's first hand-written migration:

```console
$ go run ./cmd/gorm-gate sqlmigrate blog 0001
```

And if you are going the other way — an existing database that gormgate
should adopt — see [An existing database](existing-database.md), which is
the same problem in reverse.
