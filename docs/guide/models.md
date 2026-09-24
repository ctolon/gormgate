# Models and fields

gormgate reads your models the way gorm does — through
`gorm.io/gorm/schema` — so what you already wrote is what it migrates.
Nothing in a model is gormgate-specific until you need something a gorm tag
cannot say.

## What a tag becomes

A field's Go type and its gorm tag decide the column. The mapping is gorm's
own (`Dialector.DataTypeOf`), which is why a gormgate-created schema is
identical to an `AutoMigrate`-created one.

| gorm tag | in the migration |
| --- | --- |
| `primaryKey` | `Field.PrimaryKey` |
| `autoIncrement` | `Field.AutoIncrement` |
| `not null` | `Field.Null = false` |
| `unique` | `Field.Unique` |
| `size:200` | `Field.Size` — character length for strings, bit width for numbers |
| `type:text` | `Field.Type`, verbatim |
| `default:0` | `Field.DBDefault` — a **database** default, written into the DDL |
| `comment:...` | `Field.Comment`, where the database has comments |
| `index` / `uniqueIndex` | an `Index` on the model |
| `check:name,expr` | a `CheckConstraint` |
| `constraint:OnDelete:CASCADE` | `Field.ForeignKey.OnDelete` |

Two gorm conveniences map onto Django concepts:

- `gorm.DeletedAt` becomes a nullable timestamp with an index, as gorm
  creates it.
- `autoCreateTime` / `autoUpdateTime` become `AutoNowAdd` / `AutoNow`, which
  is what the questioner uses when it has to invent a value for existing
  rows.

!!! warning "`default:` is a database default"
    A gorm `default:` tag is Django's `db_default`: it is written into the
    column definition and the database applies it. It is not the same as the
    one-off value `makemigrations` asks you for when a new column must be
    `NOT NULL` — that one fills existing rows and is then dropped again.

## What tags cannot say

Multi-column constraints, named indexes with options, table comments and a
few vendor settings have no gorm tag. Declare them by implementing
`MigrationMeta` on the model:

```go
--8<-- "doc_snippets_test.go:meta"
```

The types are re-exported from the root package so a model file does not
have to import `migrations` as well.

`Meta` also carries `Managed`: set it to `false` and gormgate will track the
model in its state but never create, alter or drop its table — for a table
some other system owns.

## Relationships

A `belongs to` relationship becomes a foreign key on the column gorm chose:

```go
type Post struct {
	AuthorID uint      `gorm:"not null"`
	Author   auth.User `gorm:"constraint:OnDelete:CASCADE"`
}
```

gives `author_id` a `ForeignKey` pointing at `auth.user`. `has many` and
`has one` need no column on this side, so they produce nothing here — the
key lives on the other model.

A `many2many` join table is tracked as a model of its own, created for you,
exactly as gorm creates it.

A relationship over a **composite** key --
`foreignKey:A,B;references:X,Y` -- cannot sit on a single field, so it
becomes a table-level `ForeignKeyConstraint` in the model's constraints,
under the name gorm gives it. Nothing extra to declare: `makemigrations`
writes it from the tag, and `AddConstraint`/`RemoveConstraint` create and
drop it. ClickHouse, which has no foreign keys at all, refuses it.

!!! note "The foreign key column is a column you declared"
    Unlike Django, where a `ForeignKey` field *is* the column and takes its
    type from the target, here `AuthorID uint` is your field with your type.
    If you change the target's key from `uint32` to `uint64`, change the
    referencing field too — the autodetector will generate the migration for
    it, but it cannot guess that you meant to.

## Table names

Table names come from gorm's naming strategy, including a `TableName()`
method if you define one. Pass the **same** `schema.Namer` to `gorm.Open`
and to `gormgate.Settings` if you customise it; if the two disagree,
gormgate will migrate tables your application does not use.

## What has no equivalent

Managers, proxy models, abstract and multi-table inheritance,
`order_with_respect_to`, swappable models and content types are Django
concepts with nothing to map onto in GORM. They are listed, with the reason
for each, in [Not applicable](../not-applicable.md).
