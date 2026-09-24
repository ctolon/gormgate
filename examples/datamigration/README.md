# datamigration — add a column, fill it, then make it `NOT NULL`

A new `NOT NULL` column cannot simply be added to a table that already has
rows: there is nothing to put in it. The fix is three operations in **one**
migration, in this order:

1. `AddField` with `Null: true` — the existing rows are accepted;
2. `RunGo` — the backfill computes a value for every row;
3. `AlterField` without `Null` — the column becomes `NOT NULL`, which is
   what the model declares.

[`shop/migrations/0002_order_currency.go`](shop/migrations/0002_order_currency.go)
is that migration. `shop_orders.currency` is derived from
`shop_orders.country`.

The backfill uses the **historical** model — `apps.GetModel("shop",
"order")` — which is the table as it stands at this point in the history,
not the `shop.Order` struct of today. On a fresh database months from now
the migration replays against that same historical shape and still works.
It is a package-level function, not a closure, because a closure has no name
to write into a generated migration file.

The example stores its data in a SQLite file so that it runs with nothing
installed; a real project would open its database server in
`internal/store`.

## Run it

```console
$ go run ./cmd/gorm-gate migrate shop 0001
applying shop.0001_initial ... ok

$ go run ./cmd/orders seed
inserted 4 orders

$ go run ./cmd/gorm-gate migrate
applying shop.0002_order_currency ... ok

$ go run ./cmd/orders list
ID  ITEM       AMOUNT  COUNTRY  CURRENCY
1   kettle     2999    DE       EUR
2   teapot     1850    GB       GBP
3   mug        900     US       USD
4   cafetiere  3400    FR       EUR
```

The rows were seeded with no currency and came out with one, so the
migration filled them on its way to `NOT NULL`.

## The SQL it runs

```console
$ go run ./cmd/gorm-gate sqlmigrate shop 0002
BEGIN;
PRAGMA foreign_keys = OFF;
--
-- add field currency to order
--
ALTER TABLE "shop_orders" ADD COLUMN "currency" text;
--
-- raw Go operation
--
-- tHIS OPERATION CANNOT BE WRITTEN AS SQL
--
-- alter field currency on order
--
CREATE TABLE "new__shop_orders" ("id" integer PRIMARY KEY AUTOINCREMENT, "item" text NOT NULL, "amount_cents" integer NOT NULL, "country" text NOT NULL, "currency" text NOT NULL);
INSERT INTO "new__shop_orders" ("id", "item", "amount_cents", "country", "currency") SELECT "id", "item", "amount_cents", "country", coalesce("currency", NULL) FROM "shop_orders";
DROP TABLE "shop_orders";
ALTER TABLE "new__shop_orders" RENAME TO "shop_orders";
PRAGMA foreign_keys = ON;
COMMIT;
```

`RunGo` is the gap in that listing — Go code has no SQL to print, so
`sqlmigrate` says so instead of pretending. (The remade table is how SQLite
changes a column's nullability; on PostgreSQL this is an `ALTER COLUMN`.)

## Reversing it

`ReverseCode` is `m.RunGoNoop`: unapplying runs the three operations
backwards, and the last of them drops the column, so there is nothing for
the backfill to undo.

```console
$ go run ./cmd/gorm-gate migrate shop 0001
rendering model states ... done
unapplying shop.0002_order_currency ... ok

$ go run ./cmd/gorm-gate showmigrations shop
shop
 [X] 0001_initial
 [ ] 0002_order_currency
```

## Starting over

```console
$ rm -f shop.sqlite3
```
