# adoptdb — take over a database gormgate did not create

The database here was built by something else:
[`cmd/legacy/schema.sql`](cmd/legacy/schema.sql), run as raw SQL. There is
no migration history to go with it, and the tables must not be dropped and
recreated. Two things make that work — `inspectdb`, which turns the schema
into models, and `migrate --fake-initial`, which accepts tables that are
already there.

SQLite keeps the example runnable with nothing installed; a real project
would open its database server in `internal/store`.

## 1. The database that is already there

```console
$ go run ./cmd/legacy
created crm.sqlite3 from schema.sql
```

## 2. inspectdb

```console
$ go run ./cmd/gorm-gate inspectdb
// This is an auto-generated gormgate model file.
// You'll have to do the following manually to clean this up:
//   * Rearrange models' order
//   * Make sure each model has one field with primaryKey
//   * Make sure each relation field has the constraint settings you want
//   * Remove `MigrationMeta` with Managed=false if you wish gormgate to create, modify and delete the table
// Feel free to rename the models, but don't rename the table names or field columns.

package models

import (
	"time"

	m "github.com/ctolon/gormgate/migrations"
)

type CrmContracts struct {
	Id         *int32 `gorm:"primaryKey;autoIncrement"`
	CustomerId int32  `gorm:"not null"` // references crm_customers(id) model CrmCustomers is defined below
	ValueCents int32  `gorm:"not null"`
}

func (CrmContracts) TableName() string { return "crm_contracts" }

func (CrmContracts) MigrationMeta() m.Meta {
	return m.Meta{
		Managed: m.Ptr(false),
	}
}

type CrmCustomers struct {
	Id       *int32 `gorm:"primaryKey;autoIncrement"`
	Name     string `gorm:"type:TEXT;not null"`
	Email    string `gorm:"type:TEXT;unique;not null"`
	SignedUp *time.Time
}

func (CrmCustomers) TableName() string { return "crm_customers" }

func (CrmCustomers) MigrationMeta() m.Meta {
	return m.Meta{
		Managed: m.Ptr(false),
	}
}
```

That output is a starting point, not a result. [`crm/models.go`](crm/models.go)
is what it became: the models renamed, `Managed: false` dropped so that
gormgate takes ownership, and the index and the foreign key that inspectdb
reported only as a comment declared as gorm tags. Table names and column
names are left alone — those are what has to keep matching the database.

## 3. Generate the initial migration and check it against the tables

```console
$ go run ./cmd/gorm-gate makemigrations crm
created crm/migrations/0001_initial.go
  + create model Customer
  + create model Contract
```

Before faking it, read what it would have run and compare it with the
schema you already have:

```console
$ go run ./cmd/gorm-gate sqlmigrate crm 0001
BEGIN;
PRAGMA foreign_keys = OFF;
--
-- create model Customer
--
CREATE TABLE "crm_customers" ("id" integer PRIMARY KEY AUTOINCREMENT, "name" TEXT NOT NULL, "email" TEXT NOT NULL, "signed_up" datetime, CONSTRAINT "uni_crm_customers_email" UNIQUE ("email"));
--
-- create model Contract
--
CREATE TABLE "crm_contracts" ("id" integer PRIMARY KEY AUTOINCREMENT, "value_cents" integer NOT NULL, "customer_id" integer NOT NULL, CONSTRAINT "fk_crm_contracts_customer" FOREIGN KEY ("customer_id") REFERENCES "crm_customers" ("id"));
CREATE INDEX "idx_crm_contracts_customer_id" ON "crm_contracts" ("customer_id");
PRAGMA foreign_keys = ON;
COMMIT;
```

Same tables, same columns, same index, same foreign key as `schema.sql`.
Where they differ, either bring the model in line or edit the migration to
describe what is actually there — `--fake-initial` only checks that the
tables exist, so a mismatch you skip past here is a mismatch you keep.

## 4. Adopt it

```console
$ go run ./cmd/gorm-gate migrate --fake-initial
applying crm.0001_initial ... faked

$ go run ./cmd/gorm-gate showmigrations
crm
 [X] 0001_initial

$ go run ./cmd/gorm-gate makemigrations
no changes detected
```

`faked` means the tables were found, so the migration was recorded as
applied without running a single statement. Nothing was created, nothing
was dropped, and the rows are untouched. `no changes detected` confirms the
models and the migration now agree.

From here the database is an ordinary gormgate project: change a model, run
`makemigrations`, run `migrate`. `--fake-initial` is for the first migration
only; everything after it is applied for real.

## Starting over

```console
$ rm -f crm.sqlite3
```
