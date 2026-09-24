# Commands

Every command is run through your project's own command:

```console
$ go run ./cmd/gorm-gate <command> [options]
```

The command line is Django 6.0's, including the help text, the option names,
the prompts and the exit codes. If you know `manage.py`, you know this.

| Command | |
| --- | --- |
| [`makemigrations`](makemigrations.md) | Write migrations for what changed in your models |
| [`migrate`](migrate.md) | Apply or unapply migrations |
| [`showmigrations`](showmigrations.md) | List migrations and which are applied |
| [`sqlmigrate`](sqlmigrate.md) | Print the SQL a migration would run |
| [`squashmigrations`](squashmigrations.md) | Replace a run of migrations with one |
| [`optimizemigration`](optimizemigration.md) | Fold one migration's operations together |
| [`sqlsequencereset`](sqlsequencereset.md) | Print SQL to reset primary-key sequences |
| [`inspectdb`](inspectdb.md) | Generate models from an existing database |
| [`check`](check.md) | Run the system checks and report what they found |

`help` lists them, `help <command>` explains one, and `version` prints the
version.

## Options every command takes

| Option | |
| --- | --- |
| `-q` | Say only what must be said. |
| `-v`, `-vv` | More detail, and more still. |
| `--settings NAME` | Which registered settings to use. |
| `--no-color` / `--force-color` | Turn styling off, or on for a non-terminal. |
| `--skip-checks` | Do not run the system checks first. (Not `check`, which *is* the checks.) |
| `-h`, `--help` | Print this command's help and exit. |

Django's `--pythonpath` and `--traceback` are gone: migrations are compiled
into your command, so there is no path to add, and an error already carries
what it needs.

## Exit codes

| Code | |
| --- | --- |
| `0` | Success. |
| `1` | The command failed — something you asked for cannot be done. |
| `2` | The command line was wrong. |
| `3` | A question could not be answered: you typed `exit` at a prompt, or `--no-input` was given and a prompt was needed. |

## Colours

Styling follows `GORMGATE_COLORS`, with the same syntax as Django's
`DJANGO_COLORS`:

```console
$ GORMGATE_COLORS="migrate_heading=cyan;bold,error=red/white" go run ./cmd/gorm-gate migrate
$ GORMGATE_COLORS=nocolor go run ./cmd/gorm-gate migrate
```
