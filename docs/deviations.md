# Intentional deviations from Django

gormgate is a faithful port of Django 6.0's migration framework to GORM. The
ported test suites under `internal/*/..._test.go` and `migrations/state_test.go`
assert *Django's* behaviour, with the exceptions listed here: these are
deliberate design decisions, and the tests assert gormgate's behaviour for them.

Each entry names the Django source it departs from and the Go tests that pin the
gormgate behaviour down. This file covers *framework semantics*; the
command line is mapped onto Django's in `docs/from-django.md`, and the
Django tests that have no gormgate counterpart at all are listed in
`docs/not-applicable.md`.

## 1. The table name is always explicit in `CreateModel`

Django derives `db_table` from `app_label` and the model name when `Meta.db_table`
is unset, and `CreateModel` therefore usually carries no table name at all
(`options` has no `db_table` key).

gormgate resolves the table name from the gorm naming strategy while building the
project state (`internal/fromgorm`), so `m.CreateModel.Table` and
`m.ModelState.Table` are *always* populated. Consequences:

- `CreateModel` always renders a `Table:` field in generated migrations.
- `ModelState.Equal` compares `Table` directly instead of comparing an options
  dictionary that may or may not hold `db_table`.
- The autodetector's `generate_altered_db_table` compares two non-empty strings.

## 2. `RenameModel` does not rename the table

In Django, a model's default `db_table` is derived from its name, so
`RenameModel.database_forwards` renames the table as a side effect.

Because gormgate always carries the table name explicitly (deviation 1), renaming
a model is a pure state operation as far as the table name goes:
`m.RenameModel` keeps `ModelState.Table` unchanged, and a table rename has to be
requested separately with `m.AlterModelTable`. The autodetector emits both
operations when the gorm naming strategy produces a different table name for the
new model name.

`RenameModel.DatabaseForwards` still calls `SchemaEditor.AlterDBTable(new, old.Table,
new.Table)` — with equal names that is a no-op — and still repoints the foreign
keys of related models, exactly like Django.

## 3. There is no `db_column`: a field's name *is* its column

Django separates `field.name` (the Python attribute) from `field.column`
(`db_column` or a derived name). gormgate's `ModelState.Fields` are keyed by the
database column name (gorm's `DBName`), and `m.ModelField.Column == m.ModelField.Name`
always.

Consequences:

- Django's `AlterField` that is emitted purely because `db_column` changed never
  occurs; `generate_renamed_fields` emits only the `RenameField`.
- Django's "renaming a field with an explicit `db_column` produces no SQL" cases
  have no counterpart.
- `ProjectState.rename_field` renames the column everywhere (unique_together,
  indexes, constraints, referencing foreign keys) because the name and the column
  are the same thing.

## 4. gorm `many2many` join tables are auto-created models

Django models a `ManyToManyField` as a field on the model, with a hidden
"through" model, and the autodetector special-cases M2M fields extensively.

gormgate has no M2M field type. `internal/fromgorm` turns each implicit gorm
`many2many:` join table into a normal `ModelState` owned by the first declaring
model's app, marked `Options.AutoCreated = true`, with a composite primary key of
two foreign-key fields. The autodetector therefore sees an ordinary model and
emits an ordinary `CreateModel`/`DeleteModel` for it — there is no `AddField`/
`RemoveField` of an M2M field, and no `through` handling.

Foreign-key fields that are part of the primary key stay inside `CreateModel`
rather than being split out into a following `AddField`, so a join table is
created in one operation.

## 5. No PostgreSQL `_like` indexes

Django's PostgreSQL backend creates an extra `varchar_pattern_ops` /
`text_pattern_ops` index (`<column>_like`) next to every indexed or unique text
column, to make `LIKE 'prefix%'` queries indexable, and its schema-editor tests
assert those statements.

gormgate's PostgreSQL backend does not create `_like` indexes: gorm's query
builder does not rely on them, and they double the index count of every text
column. Nothing in the migration state or the autodetector refers to them.

## 6. Migrations are a compiled-in registry, not imported modules

Django's `MigrationLoader` imports `<app>.migrations.<name>` modules from disk.
Go has no runtime import, so generated migration files call
`migrations.Register(&m.Migration{...})` from their `init()`, and the loader
reads that registry (`loader.Config.Registered`). The files on disk are still
scanned, but only to verify that the compiled binary matches them
(`internal/loader.checkDisk`) — a check that has no Django counterpart and that
reports a command error when the binary is stale.

`writer.Filename` also differs: a migration whose name ends in a segment the go
tool treats as a build constraint (`..._test`, `..._linux`, `..._windows_amd64`)
gets a trailing underscore so the generated file is actually compiled.

## 7. Go/Python differences in the serializer and the questioner

- `internal/writer` serializes Go values, so Django's lambda/`functools.partial`/
  `decimal`/`uuid`/`datetime`/`enum.Enum`/`LazyObject` serializer cases are ported
  to their Go equivalents (package-level functions, `time.Time`, `m.DataType`,
  `m.Custom[T]`, `m.DBValue`/`m.DBExpr`). Method values and function literals
  cannot be serialized and produce a `SerializeError`, mirroring Django's refusal
  to serialize lambdas and bound methods.
- `internal/questioner` prompts for a *Go* expression and type-checks it with
  `go/types` (`CheckExpr`) instead of `eval()`ing Python; the `time` package is
  the only one offered at the prompt. Syntax errors report `SyntaxError`, type
  mismatches report `TypeError`, and a function value of the right result type is
  accepted as a callable default.

## 8. Smaller documented differences found while porting the test suites

These came out of the ports and are asserted in the Go tests as gormgate
behaviour.

### State and operations

- **`AlterModelOptions` carries only `Managed`.** Django's operation merges an
  options *dict* and resets the keys listed in `ALTER_OPTION_KEYS` that are
  absent. gormgate has exactly one non-schema model option, so the merge
  degenerates to setting or clearing one pointer.
  (`internal/optimizer`: `TestOptimizer_CreateModelAndRemoveModelOptions`.)
- **`RenameField` keeps the field's position.** Django's
  `ProjectState.rename_field` does `fields.pop(old); fields[new] = found`, which
  moves the field to the end of the ordered dict; gormgate renames in place.
  Django is internally inconsistent here — `CreateModel.reduce` renames in place
  — no Django test asserts the move, and a stable column order is better for a
  schema. (`migrations`: `TestState_RenameFieldReferences`.)
- **Reference ordering is sorted, not insertion-ordered.** Django's
  `ProjectState.relations` is a dict in insertion order; `GetReferences` and
  `Model.RelatedFields()` sort by `(app, model)`. Same set, deterministic order.
- **`ValidateConsistency` reports a deterministic dummy node.** Django surfaces
  the first dangling dependency in insertion order; gormgate iterates keys
  sorted by `(app, name)`, so *which* `NodeNotFoundError` surfaces when several
  dependencies dangle can differ.
- **A third constraint kind, `ForeignKeyConstraint`.** Django's `ForeignKey`
  is always one field, so it has no table-level foreign key and no operation
  for one. gorm's `foreignKey:A,B;references:X,Y` builds a key over several
  columns, which cannot live on a `Field`, so gormgate adds a third
  implementation of the sealed `Constraint` interface and creates it with
  the ordinary `AddConstraint`/`RemoveConstraint` operations. A
  single-column key stays on its field, exactly as before, so no Django
  parity scenario changes. (`migrations`: `TestForeignKeyConstraint_*`;
  `itest`: `add_composite_foreign_key`, `remove_composite_foreign_key`.)
- **No `violation_error_code`.** `CheckConstraint`/`UniqueConstraint` have
  `ViolationErrorMessage` (state-only, so changing it alone is an
  `AlterConstraint`) but not Django's `violation_error_code`.
- **No `blank`.** Django skips the "you must supply a default" prompt for a
  non-null field when `field.blank and field.empty_strings_allowed`. gormgate
  has no `blank`, so a non-null string field always prompts.
- **`ModelState.Validate()`'s index message** embeds the Go value (`%#v`)
  where Django embeds `<Index: fields=['field']>`.
- **`FieldReferences` with no `to_field`** matches any referenced field name.
  This is Django's own `reference_field=None` branch, kept deliberately.

### Loader

- **`loader.Config.ReplaceMigrations` is `false` by its Go zero value**, while
  Django's `MigrationLoader.replace_migrations` defaults to `True`. A caller
  that builds a `loader.Config` by hand must therefore set it explicitly.
  The management commands do not: `Project.LoaderConfig` sets it `true` for
  every one of them, and the single override is `sqlmigrate`, which turns it
  off exactly as Django's `MigrationLoader(connection,
  replace_migrations=False)` does. A trap for new call sites.
- **Migration files are recognised by `^\d{4}_\w*\.go$`**, replacing Django's
  "ignore names starting with `_`, `~` or `.`" rule.

### Executor

- **`DetectSoftApplied` normalises the table name** with
  `Introspection.IdentifierConverter()` before comparing it against the
  introspected table names; Django compares `model._meta.db_table` directly and
  only case-folds when `ignores_table_name_case`. This is an extension for
  identifier-folding backends and is the identity elsewhere.
- **`_migrate_all_backwards` clones** the saved state for the final result where
  Django mutates it in place after `del state.apps`. Same result.

### Writer

- **Imports are a single gofmt-sorted block** sorted by import path, so
  `m "github.com/ctolon/gormgate/migrations"` sorts before `"math"` and
  `"time"` rather than being grouped stdlib-first. The output is gofmt-clean;
  only the grouping convention differs.
- **Infinities and NaN** are written as `math.Inf(1)`, `math.Inf(-1)` and
  `math.NaN()` (Go has no literal syntax for them), matching Django's
  `float("inf")`.
- **Method values and function literals cannot be serialized** and produce a
  `SerializeError` telling the author to use a package-level function; this
  mirrors Django's refusal to serialize lambdas and bound methods.
- **Map values are not composite-literal-elided** while slice elements are; both
  are valid Go, purely cosmetic.

### Backends

These follow from the rule that a gormgate-built schema must equal the one
`AutoMigrate` builds for the same models; each is verified by
`TestGormParity` for its vendor.

- **Oracle identifiers are quoted verbatim, not upper-cased and truncated.**
  Django's Oracle backend upper-cases every name, truncates it to 30
  characters and lower-cases what introspection returns. gorm-oracle writes
  the name gorm's naming strategy produced, so gormgate quotes it verbatim
  (doubling embedded `"`), matches it exactly in the catalogue queries,
  reports `ignores_table_name_case = false`, and uses Oracle 12.2's
  128-character limit. Addressing the objects gorm creates is not possible
  otherwise.
- **Oracle primary keys added by an alteration are unnamed.** Django emits a
  named constraint; gormgate emits `ALTER TABLE ... ADD PRIMARY KEY (...)`
  so that the key has the same server-generated `SYS_C...` name it would
  have had in the `CREATE TABLE`, and the migrated schema can equal a
  from-scratch one.
- **MySQL restates the comment on every `MODIFY`.** Django restates the
  nullability and the default but not the comment, which silently drops it
  when a column's type and comment change in the same operation.
- **ClickHouse accepts `not null` and auto-increment without emitting DDL**,
  because gorm's ClickHouse migrator does; the column type stays identical
  to `Dialector.DataTypeOf`. `NoColumnNullability` reports this, and the
  conformance cases that depend on a NULL value skip on it.

### Management commands

- **An invalid `GORMGATE_COLORS` entry is ignored, not fatal.** Django's
  `parse_color_setting` does `role, instructions = part.split("=")`, so a
  definition with a second `=` (`GORMGATE_COLORS="migrate_label=a=b"`) raises
  a `ValueError` and the command dies with a traceback and status 1.
  gormgate splits on the first `=` and skips a part whose instructions still
  contain one, leaving the rest of the palette in force. The Python message
  ("too many values to unpack") has no Go counterpart worth reproducing, and
  a malformed colour setting is not a reason to refuse to migrate.
  (`internal/termcolors`: `TestColorStyle_Palettes`.)
- **No `check` command, and no `display_num_errors`.** Django's
  `BaseCommand.check` can append a "System check identified N issues (M
  silenced)." summary, which only the `check` command asks for. gormgate has
  no `check` command — the checks run as part of the commands that need them
  — so `RunChecks` has no such parameter and never writes that summary.

### Error text

- **The text of a Go `error` is a Go error string**, even where the wording
  is Django's: lowercase first letter, no trailing period. This is the one
  place where gormgate's output differs from Django's. Output is
  gormgate's own throughout; see `docs/from-django.md`.
