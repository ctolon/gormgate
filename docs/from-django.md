# Feature map

If you know Django's migrations, this page tells you what the same thing is
called here, and what to do about the handful of Django concepts that have
no counterpart.

The commands are Django's and so is what they decide; what they print is
gormgate's own, and a few options are spelled the way Go command line
tools spell them. The model layer underneath differs more, because a gorm
model is not a Django model.

## The command line

Every command has the same name and does the same thing:

| Django | gormgate |
| --- | --- |
| `python manage.py makemigrations` | `gorm-gate makemigrations` |
| `migrate`, `sqlmigrate`, `showmigrations`, `squashmigrations`, `optimizemigration`, `sqlsequencereset`, `inspectdb`, `check` | the same, with the same positional arguments |

The options that are spelled differently:

| Django | gormgate | Why |
| --- | --- | --- |
| `--noinput`, `--no-input` | `--no-input`, `-y` | one spelling, and the short form Go tools use |
| `-v 0`, `-v 1`, `-v 2`, `-v 3` | `-q`, *(default)*, `-v`, `-vv` | a repeated flag rather than a level, as `go test` and `ssh` do |
| `--traceback` | *(gone)* | an error already carries what it needs; there is no stack to ask for |
| `--pythonpath` | *(gone)* | migrations are compiled into the command, so there is no path to add |
| `--settings <module>` | `--settings <name>` | the name of a settings set registered with `ExecuteSet`, not an import path |
| `DJANGO_SETTINGS_MODULE` | `GORMGATE_SETTINGS_MODULE` | |
| `DJANGO_COLORS` | `GORMGATE_COLORS` | |
| `showmigrations --list` / `-l` | *(the default)* | `--plan` still selects the other rendering |

The exit statuses are Django's, because scripts depend on them: `0` done,
`1` the command failed, `2` the command line was wrong, `3` the command
needed an answer and was told not to prompt.

What a command *prints* is not Django's. Output is lower-case and says what
happened rather than announcing what is about to:

```console
$ gorm-gate makemigrations
created blog/migrations/0001_initial.go
  + create model Post

$ gorm-gate migrate
applying blog.0001_initial ... ok
```

`itest/parity_test.go` keeps this honest from the other side: it runs the
same sequences against a real Django project and an equivalent gormgate
one and requires them to write the same migrations and to plan the same
operations in the same order.

## Models

| Django | gormgate |
| --- | --- |
| `class Post(models.Model)` | a gorm struct, registered with `gormgate.App("blog", &Post{})` |
| Abstract base class | an **embedded struct** — `gorm:"embedded"`, with `embeddedPrefix` if you want one. The fields land in the table exactly as gorm puts them, and migrations see them like any other field. |
| `Meta.db_table` | gorm's naming strategy, or a `TableName() string` method |
| `Meta.managed = False` | `Meta{Managed: gormgate.Ptr(false)}` — tracked in the state, never created or altered |
| `Meta.db_table_comment` | `Meta{DBTableComment: "..."}` |
| `Meta.unique_together` | `Meta{UniqueTogether: [][]string{{"a", "b"}}}` |
| `Meta.indexes` | `Meta{Indexes: []gormgate.Index{...}}` |
| `Meta.constraints` | `Meta{Constraints: []gormgate.Constraint{...}}` |
| `ForeignKey` | the column you declare plus gorm's `constraint:` tag |
| `ManyToManyField` | gorm's `many2many:`; the join table is tracked as a model of its own, created for you |
| `GenericForeignKey` + contenttypes | gorm's `polymorphic:` — the columns are created, without a foreign key, because the target is not one table |
| Composite primary key | gorm's composite primary key: `gorm:"primaryKey"` on more than one field |
| — (Django's `ForeignKey` is always one field) | a composite **foreign** key: `gorm:"foreignKey:A,B;references:X,Y"`, migrated as a table-level `ForeignKeyConstraint` |

### Abstract base classes

Django's abstract base has no table of its own; its fields are copied into
every model that inherits it. A gorm embedded struct does the same thing,
and migrations see the fields exactly as gorm lays them out.

=== "Django"

    ```python
    class Timestamps(models.Model):
        created_at = models.BigIntegerField()
        updated_at = models.BigIntegerField()

        class Meta:
            abstract = True


    class Comment(Timestamps):
        body = models.CharField(max_length=500)
    ```

=== "gormgate"

    ```go
    --8<-- "doc_snippets_test.go:abstractbase"
    ```

Add `gorm:"embedded;embeddedPrefix:meta_"` if you want the columns
prefixed.

## Fields

| Django | gormgate |
| --- | --- |
| `null=True` | the absence of `not null` in the gorm tag |
| `db_default=...` | gorm's `default:` tag — written into the column definition |
| `default=...` (one-off) | what `makemigrations` asks you for when a new column must be `NOT NULL`; it fills existing rows and is then dropped |
| `auto_now_add` / `auto_now` | gorm's `autoCreateTime` / `autoUpdateTime` |
| `db_comment` | gorm's `comment:` tag |
| `unique=True` | gorm's `unique` tag |
| `db_index=True` | gorm's `index` tag |
| `GeneratedField` | gorm's `->` / generated column tags, where the database has them |
| `db_column="..."` | gorm's `column:` tag. The Go field name and the column may differ, and renaming the Go field with the tag pinned produces no migration — see below for the one thing that differs. |

## Indexes and constraints

| Django | gormgate |
| --- | --- |
| `Index(fields=[...])` | `Index{Fields: gormgate.Columns("a", "b")}` |
| `Index(fields=["-created"])` | `IndexField{Column: "created", Sort: gormgate.SortDesc}` |
| `Index(Lower("title"))` | `IndexField{Expression: "lower(title)"}` |
| `Index(condition=Q(...))` | `Index{Where: "published"}` |
| `Index(include=[...])` | `Index{Include: []string{...}}` |
| `Index(opclasses=[...])` | `Index{OpClasses: []string{...}}` |
| `CheckConstraint(condition=...)` | `&CheckConstraint{Name: ..., Check: "views >= 0"}` |
| `UniqueConstraint(fields=[...])` | `&UniqueConstraint{Name: ..., Fields: []string{...}}` |
| `UniqueConstraint(condition=Q(...))` | `&UniqueConstraint{Condition: "..."}` |
| `UniqueConstraint(deferrable=...)` | `&UniqueConstraint{Deferrable: gormgate.Deferred}` |
| `UniqueConstraint(nulls_distinct=False)` | `&UniqueConstraint{NullsDistinct: gormgate.Ptr(false)}` |
| `UniqueConstraint(Lower("name"))` | a **unique expression index**: `Index{Unique: true, Fields: []IndexField{{Expression: "lower(name)"}}}` |

Which of these a given database actually supports is in
[Databases](vendors.md); an operation a backend cannot perform fails with a
`NotSupportedError` that names the operation, the model and the reason,
rather than emitting SQL that will not run.

## Operations

Every core operation is ported under the same name: `CreateModel`,
`DeleteModel`, `RenameModel`, `AlterModelTable`, `AlterModelTableComment`,
`AlterUniqueTogether`, `AlterModelOptions`, `AddField`, `RemoveField`,
`AlterField`, `RenameField`, `AddIndex`, `RemoveIndex`, `RenameIndex`,
`AddConstraint`, `RemoveConstraint`, `AlterConstraint`, `RunSQL`,
`SeparateDatabaseAndState`.

=== "Django"

    ```python
    def backfill(apps, schema_editor):
        Article = apps.get_model("blog", "Article")
        for row in Article.objects.filter(slug=""):
            row.slug = slugify(row.title)
            row.save(update_fields=["slug"])


    operations = [
        migrations.AddField("article", "slug", models.CharField(max_length=200, null=True)),
        migrations.RunPython(backfill, migrations.RunPython.noop),
        migrations.AlterField("article", "slug", models.CharField(max_length=200)),
    ]
    ```

=== "gormgate"

    ```go
    --8<-- "doc_snippets_test.go:datamigration"
    ```

    ```go
    --8<-- "doc_snippets_test.go:backfilloperations"
    ```

Two differences:

- **`RunPython` is `RunGo`.** Same shape, same `hints`, `atomic` and
  `elidable`; the function is given historical models instead of historical
  classes. See [Data migrations](guide/data-migrations.md).
- **`AlterModelOptions` carries only `Managed`.** Django tracks a dozen
  `Meta` options in migrations — `ordering`, `verbose_name`, `permissions`,
  `get_latest_by` and the rest. None of them reaches the database, and none
  has a gorm counterpart, so there is nothing for a migration to record.

## Supported, but differently

### `db_column`

gorm's `column:` tag does what `db_column` does, and gormgate honours it:
the migration names the field by its **column**, so renaming the Go field
while the tag stays put changes nothing.

=== "Django"

    ```python
    class Person(models.Model):
        full_name = models.CharField(max_length=100, db_column="name")
    ```

=== "gormgate"

    ```go
    type Person struct {
    	ID       uint   `gorm:"primaryKey"`
    	FullName string `gorm:"column:name;size:100"`
    }
    ```

Both describe a column called `name`, and in both you can rename the
attribute to `Surname` without a migration.

The difference is in the other direction. Django's state carries the field
name *and* the column, so changing `db_column` is an `AlterField`.
gormgate's state is keyed by the column alone, so changing the `column:`
tag looks like a rename: the autodetector will ask

```
Did you rename person.name to person.legal_name (a CharField)? [y/N]
```

Answer `y` and you get the `RenameField` you wanted.

### Composite foreign keys

gorm declares a composite **primary** key with `primaryKey` on more than
one field, and a composite **foreign** key with
`foreignKey:A,B;references:X,Y`. gormgate migrates both.

A foreign key over one column lives on the field it constrains
(`Field.ForeignKey`). A key over several columns cannot: it becomes a
table-level `m.ForeignKeyConstraint` in `Meta.Constraints`, beside the
unique and check constraints, and `AddConstraint` / `RemoveConstraint`
create and drop it.

```go
--8<-- "doc_snippets_test.go:compositefk"
```

`makemigrations` writes the constraint gorm would have created, under
gorm's own name:

```go
--8<-- "doc_snippets_test.go:compositefkop"
```

Django has no counterpart operation: its `ForeignKey` is always one field,
so there is nothing to be in parity with here, only with gorm.

The one backend that refuses it is ClickHouse, which has no foreign keys at
all; it refuses a field-level one the same way.

## No equivalent

These are the Django features that do not exist here. Each is a deliberate
stop, not an oversight, and each has something to do instead.

### Proxy models

A Django proxy model is a second Python class over one table, with
different managers and ordering. It has no schema of its own.

**Instead:** declare a second Go type with the same `TableName()` and mark
it unmanaged. gormgate tracks it, so migrations know it exists, and never
creates or alters its table.

=== "Django"

    ```python
    class PublishedPost(Post):
        class Meta:
            proxy = True
            ordering = ["-published_at"]
    ```

=== "gormgate"

    ```go
    --8<-- "doc_snippets_test.go:unmanaged"
    ```

`sqlmigrate` shows what that costs:

```console
--
-- create model PublishedPost
--
-- (no-op)
```

Only *managed* models own a table, which is the rule Django applies too, so
this is also how you map a database view alongside the table it reads
from.

### Multi-table inheritance

Django's MTI gives a child model its own table plus an implicit
one-to-one key to the parent's. gorm has no notion of a parent model, so
there is nothing for the implicit key to point at.

**Instead:** declare both models and the one-to-one relationship
explicitly. That is what MTI generates anyway, and here it is visible in
your code rather than implied.

=== "Django"

    ```python
    class Place(models.Model):
        name = models.CharField(max_length=100)
        address = models.CharField(max_length=200)


    class Restaurant(Place):          # implicit place_ptr OneToOneField
        serves_pizza = models.BooleanField()
    ```

=== "gormgate"

    ```go
    --8<-- "doc_snippets_test.go:mti"
    ```

### Managers

Managers are a query-layer concept — Django tracks them in migrations only
because `makemigrations` wants a complete picture of the model. They have
no schema. gorm's scopes do the same job and need no migration.

=== "Django"

    ```python
    class PublishedManager(models.Manager):
        def get_queryset(self):
            return super().get_queryset().filter(published_at__isnull=False)


    class Post(models.Model):
        objects = models.Manager()
        published = PublishedManager()
    ```

=== "gormgate"

    ```go
    --8<-- "doc_snippets_test.go:scope"
    ```

    Used as `db.Scopes(published).Find(&posts)`.

### Swappable models (`AUTH_USER_MODEL`)

Django lets a setting decide which model a foreign key points at, resolved
lazily at import time. Go resolves types at compile time.

**Instead:** make the choice in the code that builds `Settings`. The
migration that results names a concrete model, which is what ends up in the
database either way.

=== "Django"

    ```python
    # settings.py
    AUTH_USER_MODEL = "accounts.User"

    # models.py
    class Post(models.Model):
        author = models.ForeignKey(settings.AUTH_USER_MODEL, on_delete=models.CASCADE)
    ```

=== "gormgate"

    ```go
    --8<-- "doc_snippets_test.go:swappable"
    ```

What you lose is Django's lazy `"app.Model"` reference, which lets a
migration be written before the model is chosen. In Go the foreign key
names a type, so the choice has to be made before the migration is
generated.

### contenttypes

Django's contenttypes app is a table of installed models, used by generic
relations and permissions. gormgate has no app registry at run time to fill
it from.

**Instead:** for a generic relation, use gorm's `polymorphic:` tag, which
gormgate migrates. For anything else that wanted the table, declare it as
an ordinary model.

=== "Django"

    ```python
    class Attachment(models.Model):
        content_type = models.ForeignKey(ContentType, on_delete=models.CASCADE)
        object_id = models.PositiveIntegerField()
        owner = GenericForeignKey("content_type", "object_id")
        filename = models.CharField(max_length=200)
    ```

=== "gormgate"

    ```go
    --8<-- "doc_snippets_test.go:polymorphic"
    ```

The difference is what the type column holds: Django stores a foreign key
into `django_content_type`, gorm stores the model's name. gormgate creates
both columns and the indexes, and no foreign key — there is no single table
to point at.

### `order_with_respect_to`

Django adds an `_order` integer column and an ordering API over it.

**Instead:** declare the column. It is one field, and you then own its
semantics — including the uniqueness Django adds with it.

=== "Django"

    ```python
    class Chapter(models.Model):
        book = models.ForeignKey(Book, on_delete=models.CASCADE)

        class Meta:
            order_with_respect_to = "book"   # adds an _order column
    ```

=== "gormgate"

    ```go
    --8<-- "doc_snippets_test.go:ordering"
    ```

What you do not get is Django's `get_next_in_order()` / `set_order()` API,
which is query-layer code rather than schema.

## PostgreSQL-specific operations

Django ships `django.contrib.postgres.operations` for the things only
PostgreSQL can do. gormgate ports them as `migrations/postgres`:

```go
import pg "github.com/ctolon/gormgate/migrations/postgres"
```

| Django | gormgate |
| --- | --- |
| `CreateExtension("hstore")` | `&pg.CreateExtension{Name: "hstore"}` |
| `HStoreExtension()`, `TrigramExtension()`, … | `pg.HStoreExtension()`, `pg.TrigramExtension()`, … — functions returning a `*CreateExtension` with the name filled in |
| `AddIndexConcurrently` / `RemoveIndexConcurrently` | `&pg.AddIndexConcurrently{...}` / `&pg.RemoveIndexConcurrently{...}` |
| `CreateCollation` / `RemoveCollation` | `&pg.CreateCollation{...}` / `&pg.RemoveCollation{...}` |
| `AddConstraintNotValid` / `ValidateConstraint` | `&pg.AddConstraintNotValid{...}` / `&pg.ValidateConstraint{...}` |

=== "Django"

    ```python
    from django.contrib.postgres.operations import AddIndexConcurrently, TrigramExtension


    class Migration(migrations.Migration):
        atomic = False
        operations = [
            TrigramExtension(),
            AddIndexConcurrently("article", GinIndex(fields=["title"], name="idx_article_title")),
        ]
    ```

=== "gormgate"

    ```go
    --8<-- "doc_snippets_test.go:pgoperations"
    ```

Two things behave the way Django's do and are easy to trip over:

- **They do nothing on another database.** Like Django's, each operation
  checks the vendor and returns without running anything, so a migration
  that creates an extension is harmless on SQLite. What counts as "another
  database" is per operation and was verified against the real servers:
  extensions and collations are PostgreSQL only — CockroachDB has not
  implemented them, and openGauss can create an extension but not drop one,
  which would make the operation irreversible — while concurrent indexes
  and `NOT VALID` constraints work on CockroachDB and openGauss too.
- **The concurrent ones refuse to run inside a transaction**, because
  PostgreSQL does. Set `Atomic: m.Ptr(false)` on the migration; the error
  says so if you forget.

There is **no `RemoveExtension`**: Django does not have one either.
Dropping an extension is what `CreateExtension` does when the migration is
unapplied.

## Beyond Django

A few things here have no Django counterpart, because gorm or Go needs
them:

- **Migrations are compiled in.** There is no runtime import in Go, so
  `makemigrations` maintains the file that imports every migrations
  package, and the loader refuses to run against a binary that disagrees
  with what is on disk.
- **`Meta.ClickHouse`** carries the table engine, `ORDER BY` and settings
  that only ClickHouse has.
- **Ten databases**, including four Django has no backend for: TiDB,
  GaussDB/openGauss, Oracle through gorm, and ClickHouse.
