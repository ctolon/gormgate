# Limitations and open work

Everything gormgate knowingly does not do, in one place, with what to do
about it. Nothing here is a surprise waiting to be found: each item is
either a deliberate stop, a debt with a workaround, or something that has
simply not been exercised yet.

For the Django concepts that have no counterpart at all — proxy models,
multi-table inheritance, managers, contenttypes — see the
[Feature map](from-django.md), which gives the recipe for each.

## Commands Django has and gormgate does not

| Command | Why not |
| --- | --- |
| `dumpdata` / `loaddata` | Fixtures are a serialization framework, not a migration one. A data migration (`RunGo`) does the same job in a way that is versioned with the schema it depends on. |
| `sqlflush` / `flush` | Emptying every table is a test-harness concern. `RunSQL` can do it in a migration, and a test suite is better served by creating a fresh database — which is what gormgate's own integration suite does. |
| `dbshell` | Opens the database's own client. It is a wrapper around a binary gormgate does not manage. |
| `createcachetable` | Django's cache framework. There is nothing in gorm it corresponds to. |

The migration commands themselves are all here, with Django's options; see
[Commands](commands/index.md).

## Deliberate limits of the port

These are decisions, each with its reason recorded:

- **Only `Managed` survives in `AlterModelOptions`.** Django tracks a dozen
  `Meta` options in migrations — `ordering`, `verbose_name`, `permissions`
  and the rest. None of them reaches the database and none has a gorm
  counterpart.
- **ClickHouse is a documented subset.** It has no foreign keys, no unique
  constraints, no plain indexes and no column nullability, so the
  operations that need those are refused with an error naming the
  operation, the model and the reason rather than being silently skipped.
  [Databases](vendors.md) has the full list per backend.
- **`inspectdb`'s output cannot be compared with Django's.** One prints
  Python classes and the other Go structs. Its behaviour is covered by the
  end-to-end suite, which runs it against a real schema and compiles what
  it prints.
- **The error text of a Go `error` is a Go error string**, lower case and
  without a trailing period, where Django's is a sentence. This is the one
  place the CLI's text differs from Django's. What a command prints is
  gormgate's own and is not compared against Django at all; see
  [Feature map](from-django.md).
- **`check --tag compatibility` reads the models, not the migrations.** It
  reports what the current model definitions declare and the named database
  will not create, which is what `CreateModel` would do with them. An
  alteration a backend refuses only once a column exists is outside what a
  model can show, so only the one gormgate can predict from a model is
  reported (`gormgate.W002`, TiDB's clustered integer primary key). See
  [check](commands/check.md).

## Debts in the code

Real, known, and not currently causing harm:

- **Several ported functions are long** — `base.Editor.PerformAlterField`
  is 255 lines, `sqlite3.remakeTable` 183 — because they are line-for-line
  ports of equally long Django methods. Splitting them would break the
  property that makes the port auditable against its original, and
  `_alter_field` in particular is a sequence of ordered side effects where
  extraction risks reordering DDL.
- **`Objects`, the data-migration API, is `map[string]any`.** This is
  inherent rather than sloppy: a historical model has no Go type by
  construction — that is what makes it historical — so there is nothing for
  a generic parameter to bind to. The column names are checked against the
  model before any SQL is built.
- **`Field.Default` is `any`.** A default is whatever the user's struct
  field holds, so there is no closed set to name; `migrations.Register`
  validates its shape at init, which turns what was a mid-migration failure
  into a startup one. (`RunSQL.SQL` used to be `any` too and is now the
  sealed `migrations.SQL`.)
- **A few state accessors panic** where their siblings return an error
  (`ProjectState.mustModel`, `Apps.MustModel`, `MustApps`). They are
  invariants a caller is meant to have checked, so a panic is a bug in
  gormgate; the command boundary catches it and reports it as an internal
  error with a stack, rather than letting it reach the runtime.

## Not yet exercised

Honest unknowns rather than known problems:

- **CI has never run.** The workflows are written and the same commands
  pass locally, but no push has happened, so the runner environment is
  unproven — in particular the integration matrix, which starts a database
  service per vendor, and the Oracle job, which pulls a large image.
- **The upstream canary has never fired.** It is scheduled weekly against
  the latest Django and CPython; see [Upgrading
  Django](upgrading-django.md) for what to do when it goes red.
- **No performance figures.** The suites prove correctness on schemas of
  tens of tables. Nobody has measured `makemigrations` or `migrate` against
  a schema of hundreds, and the autodetector is O(models × fields) in
  several passes.
- **Integration tests run on Linux only.** The unit tests run on Linux,
  macOS and Windows; the database suites need Docker and run on Linux.

## Not planned

- **A query layer.** gormgate migrates schemas; gorm queries them. There is
  no intention to wrap gorm.
- **Django's app registry, settings module or signals** beyond the
  migration ones. `Settings` is a struct a program fills in, not a module
  resolved by name.
