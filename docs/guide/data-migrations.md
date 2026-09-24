# Data migrations

Sometimes a schema change needs rows to change with it: fill a new column,
split a name into two, normalise a value before a constraint can be added.
That is what `RunGo` is for.

```go
&m.RunGo{
	Code:        backfillSlugs,
	ReverseCode: m.RunGoNoop,
},
```

with the function somewhere in the same package:

```go
--8<-- "doc_snippets_test.go:datamigration"
```

## Historical models, not your structs

`apps.GetModel("blog", "post")` does not return `blog.Post`. It returns the
model **as it was at this point in the migration history** — the columns
that existed then, with the types they had then.

This matters more than it looks. A data migration written today runs on a
fresh database months from now, replaying history from the start. If it used
`blog.Post`, it would see columns that do not exist yet, and break. The
historical model sees exactly what the database has at that moment, and
`Objects` refuses a column the model does not have rather than generating
SQL that fails halfway.

Use the migration's own package-level functions, not closures: a closure
cannot be written into a generated file, and gormgate will tell you so.

## The Objects API

| Method | |
| --- | --- |
| `Create(rows ...map[string]any) error` | insert |
| `Find(where map[string]any) ([]map[string]any, error)` | select |
| `Count(where map[string]any) (int64, error)` | count |
| `Update(where, values map[string]any) (int64, error)` | update the matching rows |
| `Delete(where map[string]any) (int64, error)` | delete the matching rows |
| `UpdateAll(values map[string]any) (int64, error)` | update **every** row |
| `DeleteAll() (int64, error)` | delete **every** row |
| `DB() *gorm.DB` | the handle, for anything the above cannot express |

`Update` and `Delete` refuse an empty filter. Wiping a table is something
you have to ask for by name, so that a filter map which happened to come out
empty cannot do it by accident.

Everything runs on the migration's own connection, inside its transaction
where the database has one — so a failure rolls the data change back
together with the schema change.

## Reversing

`ReverseCode` runs when the migration is unapplied. Three choices:

- a function that undoes the change,
- `m.RunGoNoop` when unapplying needs nothing (usually because the column is
  about to be dropped anyway),
- nothing at all, which makes the migration irreversible — `migrate` to an
  earlier point will then stop and say which operation blocked it.

## Order

Operations run in the order they are listed, so a `RunGo` that fills a
column goes *after* the `AddField` that creates it and *before* the
`AlterField` that makes it `NOT NULL`:

```go
--8<-- "doc_snippets_test.go:backfilloperations"
```

`makemigrations --empty` gives you a file to write this into, and you can
move the generated `AddField`/`AlterField` into it.

## Elidable

`IsElidable: true` marks a `RunGo` that may be dropped when the migration is
squashed — a one-off backfill that a fresh database will never need, because
the column it fills is created empty there. Leave it false if the data
change must survive squashing.

## RunSQL for the same job

When the change is expressible in one statement, `RunSQL` is simpler and
runs in the database rather than round-tripping rows:

```go
&m.RunSQL{
	SQL:        m.Script("UPDATE blog_posts SET slug = lower(title) WHERE slug = ''"),
	ReverseSQL: m.NoSQL,
},
```

The trade-off is portability: that statement is yours to keep working on
every database you target, while `RunGo` goes through gorm.
