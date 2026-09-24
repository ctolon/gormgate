# multidb — two databases and a router

The project declares two databases, `default` and `reports`, and a
[`reportsRouter`](cmd/gorm-gate/router.go) that keeps the `analytics` app on
`reports` and every other app off it:

```go
func (reportsRouter) AllowMigrate(db, app, model string, hints map[string]any) *bool {
	switch {
	case app == "analytics":
		return gormgate.Ptr(db == "reports")
	case db == "reports":
		return gormgate.Ptr(false)
	default:
		return nil
	}
}
```

`nil` means "no opinion" — the next router decides, and if none does, the
model is migrated.

Two SQLite files stand in for two servers so that the example runs with
nothing installed; a real project would open two database servers in
`internal/store`.

## Run it

Every migration is applied against **both** databases. What the router
changes is what each one actually does.

```console
$ go run ./cmd/gorm-gate migrate
applying analytics.0001_initial ... ok
applying shop.0001_initial ... ok

$ go run ./cmd/gorm-gate migrate --database reports
applying analytics.0001_initial ... ok
applying shop.0001_initial ... ok

$ go run ./cmd/tables
default  shop_orders
reports  analytics_page_views
```

Both runs say `applying analytics.0001_initial ... ok`, and only one of them
created `analytics_page_views`. On `default` the router refused the
`analytics` model, so the migration ran no DDL.

## What showmigrations reports

Per database — and the answer is the same on both, because a migration the
router skipped is still **recorded**:

```console
$ go run ./cmd/gorm-gate showmigrations
analytics
 [X] 0001_initial
shop
 [X] 0001_initial

$ go run ./cmd/gorm-gate showmigrations --database reports
analytics
 [X] 0001_initial
shop
 [X] 0001_initial
```

That is the point of recording it: each database's history is complete, so
neither one will try to replay a migration it has already been carried past.
To see where the two databases differ, ask for the SQL:

```console
$ go run ./cmd/gorm-gate sqlmigrate --database reports analytics 0001
BEGIN;
PRAGMA foreign_keys = OFF;
--
-- create model PageView
--
CREATE TABLE "analytics_page_views" ("id" integer PRIMARY KEY AUTOINCREMENT, "path" text NOT NULL, "seen" datetime);
CREATE INDEX "idx_analytics_page_views_path" ON "analytics_page_views" ("path");
PRAGMA foreign_keys = ON;
COMMIT;

$ go run ./cmd/gorm-gate sqlmigrate --database reports shop 0001
BEGIN;
PRAGMA foreign_keys = OFF;
--
-- create model Order
--
-- (no-op)
PRAGMA foreign_keys = ON;
COMMIT;
```

`-- (no-op)` is the router saying no.

## Which commands take `--database`

The ones that talk to a database: `migrate`, `sqlmigrate`,
`showmigrations`, `sqlsequencereset`, `inspectdb`. `makemigrations` does
not — it works from your models and your migration files, not from any
database.

## Starting over

```console
$ rm -f default.sqlite3 reports.sqlite3
```
