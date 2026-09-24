# Multiple databases

A project can declare more than one database, choose which app goes where
with a router, and hang code off the start and end of a migrate run.

## Declaring them

```go
Databases: map[string]gormgate.Database{
	"default": {Open: func(ctx context.Context) (*gorm.DB, error) { /* ... */ }},
	"reports": {Open: func(ctx context.Context) (*gorm.DB, error) { /* ... */ }},
},
```

`"default"` is required. The commands that talk to a database — `migrate`,
`sqlmigrate`, `showmigrations`, `sqlsequencereset` and `inspectdb` — take
`--database` to pick another. `makemigrations`, `squashmigrations` and
`optimizemigration` do not, because they work from your models and your
migration files alone. `check` takes it too, but the other way round: it
opens no connection unless you name one, and may be given several:

```console
$ go run ./cmd/gorm-gate migrate --database reports
$ go run ./cmd/gorm-gate sqlmigrate --database reports analytics 0003
```

`Open` is called at most once per command, the first time that database is
needed, with the context passed to `Execute`/`CallCommand` — so cancelling
that context cancels a migration in progress.

## Routers

A router decides whether a model may be migrated on a database. It is
Django's `allow_migrate`, with `nil` meaning "no opinion, ask the next
router":

```go
--8<-- "example_test.go:router"
```

```go
Routers: []gormgate.Router{reportsRouter{}},
```

That keeps `analytics` on `reports` and everything else off it. Routers are
consulted per model, so a migration can be partly applied on one database
and skipped on another — and it is still **recorded** on both, which is what
makes `showmigrations --database` meaningful everywhere.

The hints map carries what Django's does: `model_name` and `model` for a
schema operation, and whatever you put in `RunSQL.Hints` / `RunGo.Hints` for
those.

## Hooks around migrate

`PreMigrate` and `PostMigrate` are gormgate's equivalent of Django's
`pre_migrate` and `post_migrate` signals, keyed by app label:

```go
PostMigrate: map[string][]gormgate.Hook{
	"blog": {func(r gormgate.MigrateRun) error {
		if r.Verbosity >= 1 {
			fmt.Printf("blog is now at %d migrations\n", len(r.Plan))
		}
		return nil
	}},
},
```

`MigrateRun` carries what the signal carries: the `App`, the `Conn` the
migration ran on, the `Plan`, the `Verbosity`, and `Interactive` — which is
false when the command was given `--no-input`, so a hook knows not to prompt.

A hook that returns an error stops the command.

## Named settings

For a project that runs against different configurations — say local and
staging — register a set and pick one:

```go
func main() {
	gormgate.ExecuteSet(gormgate.SettingsSet{
		"local":   localSettings(),
		"staging": stagingSettings(),
	}, "local")
}
```

```console
$ go run ./cmd/gorm-gate migrate --settings staging
$ GORMGATE_SETTINGS_MODULE=staging go run ./cmd/gorm-gate migrate
```

`--settings` wins over the environment variable, which wins over the default
name you passed — the same precedence Django's `--settings` has.
