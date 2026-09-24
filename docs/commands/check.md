# check

Runs the system checks and reports what they found. Every other command runs
the same checks before it does its work, unless you pass `--skip-checks`;
this one runs them on their own, so you can put it in CI or in a pre-commit
hook.

```console
$ go run ./cmd/gorm-gate check
System check identified no issues (0 silenced).
```

The clean-run line goes to **stdout**. Anything the checks found goes to
**stderr**, grouped by severity:

```console
$ go run ./cmd/gorm-gate check
System check identified some issues:

ERRORS:
blog.Post: (gormgate.E311) relation Author references model User, which is not registered in any app

System check identified 1 issue (0 silenced).
```

An error fails the command with exit status 1. Silence one you have decided
to live with by listing its ID in `Settings.SilencedChecks`; it is then
counted in the footer instead of reported.

## Tags

Each check carries a tag saying what it checks. `--tag` runs only the checks
with that tag and may be repeated; `--list-tags` lists them.

```console
$ go run ./cmd/gorm-gate check --list-tags
compatibility
database
models
```

| Tag | |
| --- | --- |
| `models` | The models of the project must convert to a valid state whose relations all resolve to a registered model. |
| `database` | The connection must open, which is where a backend's own guards run: a supported server version, and — on openGauss — a database in PostgreSQL compatibility mode. |
| `compatibility` | The naming strategy gormgate is configured with must be the one the application's gorm handle uses, and every field, index and constraint the models declare must be something the named database will actually create. |

The `database` and `compatibility` checks only run against the aliases
`--database` names, which is Django's rule for its own: a bare `check` never
opens a connection, so it works with the database down.

```console
$ go run ./cmd/gorm-gate check --database default
```

A connection that cannot be opened is reported as a check message, not as a
crash:

```console
$ go run ./cmd/gorm-gate check --database default
System check identified some issues:

ERRORS:
default: (gormgate.E001) BLOGPROJ_DSN is not set

System check identified 1 issue (0 silenced).
```

## `compatibility`: what your databases will refuse

`--tag compatibility` answers the question `migrate` otherwise answers
halfway through: *will this actually work on that database?* It needs a
connection, so it runs against the aliases `--database` names.

### The naming strategy

`Settings.NamingStrategy` must be the naming strategy the application passes
to `gorm.Open`. When the two differ, nothing fails — gormgate migrates
tables, columns and constraints under names the application never queries,
and the divergence is total and silent. The check compares the two
strategies **by behaviour**, not by identity: it runs both over probe inputs
covering `TableName`, `ColumnName`, `JoinTableName`, `RelationshipFKName`,
`CheckerName`, `IndexName` and `UniqueName`, and reports the first input they
answer differently, with both answers. Two different types that agree on
every probe are accepted.

```console
$ go run ./cmd/gorm-gate check --tag compatibility --database default
System check identified some issues:

ERRORS:
default: (gormgate.E002) Settings.NamingStrategy is not the naming strategy of the gorm handle of database 'default': TableName("Post") is "posts" for Settings.NamingStrategy and "app_posts" for the connection
	HINT: Pass the same schema.Namer to gorm.Open and to gormgate.Settings.NamingStrategy. Otherwise gormgate migrates tables, columns and constraints under names the application never uses.
```

### What the backend will not do

Every declared field, index and constraint is walked against the backend's
feature flags. The two outcomes are reported apart, because they are
genuinely different:

| | |
| --- | --- |
| **`gormgate.E003`**, error | The backend's own schema editor raises `migrations.NotSupportedError` and **the migration stops**. ClickHouse does this for foreign keys, unique columns, unique indexes, `unique_together`, `UniqueConstraint`, `ForeignKeyConstraint` and any index without an explicit `Index.Type`; Oracle does it for a foreign key with an `ON UPDATE` action. |
| **`gormgate.W001`**, warning | The base schema editor skips the object silently: `migrate` succeeds and **the schema simply does not have it**. A foreign key where the backend has none, a comment where it has no comments, a check constraint, a partial, covering or expression index, a unique constraint the backend cannot express in full — each is reported with what will be missing. |
| **`gormgate.W002`**, warning | The model migrates as it stands, but the backend will refuse a later change to it: TiDB cannot change the type of a clustered integer primary key. |

```console
$ go run ./cmd/gorm-gate check --tag compatibility --database default
System check identified some issues:

ERRORS:
blog.Post: (gormgate.E003) database 'default' (ClickHouse) refuses this model: ClickHouse has no foreign keys, but column "author_id" declares one
	HINT: The migration stops with this error. Change the model, or keep it off this database with a router.
```

A model a router keeps off the database is not reported for it, and a model
the backend refuses outright is not also reported for what would have been
skipped inside it: the table is not created at all.

### The inventory

Naming the tag also prints, at `INFO` level, what of the project is
gormgate's — the migrations directories and how many files each holds, the
generated `zz_gormgate_migrations.go`, and the `gormgate_migrations` table of
each database — together with the fact that your model structs are plain
gorm and need no change to leave. [Leaving gormgate](../guide/leaving.md)
walks through it.

```console
$ go run ./cmd/gorm-gate check --tag compatibility
System check identified some issues:

INFOS:
gormgate: (gormgate.I001) gormgate owns nothing in this project but the following:
	  blog/migrations (blog, 3 migration files)
	  cmd/gorm-gate/zz_gormgate_migrations.go
	  the 'gormgate_migrations' table of database 'default'
	HINT: Your model structs are plain gorm and need no change to stop using gormgate: delete the files above, drop the table, and the models keep working.

System check identified 1 issue (0 silenced).
```

It is reported **only** when `--tag compatibility` names the tag. It is
information, not a problem: a bare `check` on a healthy project prints
`System check identified no issues (0 silenced).` and nothing else.

## Which messages fail the command

`--fail-level` sets the severity at or above which a message makes the
command exit non-zero. The default is `ERROR`, so warnings are printed and
forgiven; `--fail-level WARNING` makes them fatal, which is what you want in
CI.

```console
$ go run ./cmd/gorm-gate check --fail-level WARNING
```

## Limiting the run

Positional arguments are app labels: only those apps' models are checked. A
problem that belongs to no single app is reported whichever apps you named.

```console
$ go run ./cmd/gorm-gate check blog
```

<!-- BEGIN help -->

```console
$ gorm-gate help check
Run the system checks and report what they find. With no apps named,
every app is checked.

Usage:
  gorm-gate check [app ...] [flags]

Flags:
      --database stringArray   run the checks that need a connection against this database; repeatable
      --deploy                 include the deployment checks
      --fail-level level       exit non-zero at this message level or above (default error)
  -h, --help                   help for check
      --list-tags              list the tags the checks carry
  -t, --tag stringArray        run only the checks carrying this tag; repeatable

Global Flags:
      --force-color       colorize the output even when it is not a terminal
      --no-color          do not colorize the output
  -q, --quiet             only say what must be said
      --settings string   settings to use, as registered with gormgate.ExecuteSet
      --skip-checks       do not run the system checks first
  -v, --verbose count     more detail; repeat for more still
```

<!-- END help -->

## Notes

- `check` does not take `--skip-checks`: the command *is* the checks, and
  Django leaves the option off for the same reason.
- `--deploy` is accepted because Django has it, and registers nothing.
  Django's deployment checks are about Django's own settings — `SECRET_KEY`,
  `DEBUG`, `ALLOWED_HOSTS`, the security middleware — none of which gormgate
  has. Passing it changes neither the checks that run nor the tags
  `--list-tags` prints.
- The `compatibility` tag has no Django counterpart to port. Django has a
  tag of that name, so the command line stays Django's, but its checks are
  about Django's own settings; gormgate's are about the gorm naming strategy
  and the backends.
- An app label the project does not have is reported like any other
  command failure, and exits 1.
