# optimizemigration

Runs the optimizer over a single migration and rewrites it in place.

This is what `squashmigrations` does to the operations it merges, exposed on
its own: a `CreateModel` followed by an `AddField` becomes one `CreateModel`,
an `AddField` followed by a `RemoveField` of the same field disappears, and
so on.

```console
$ go run ./cmd/gorm-gate optimizemigration thing 0001
Optimizing from 3 operations to 1 operations.
Optimized migration .../thing/migrations/0001_initial.go
```

<!-- BEGIN help -->

```console
$ gorm-gate help optimizemigration
Fold a migration's operations into the smallest set that has the same
effect, and write it back in place.

Usage:
  gorm-gate optimizemigration app migration [flags]

Flags:
      --check   exit non-zero if the migration can be optimized, without writing it
  -h, --help    help for optimizemigration

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

- The path it prints is absolute; it is elided above.
- `--check` prints the same first line and exits non-zero if the migration
  could be optimized, without writing anything. A migration that is already
  optimal prints `no optimizations possible` and exits 0.
- Optimizing an *applied* migration does not change what the database
  already has; it changes what a fresh database will do. Keep that in mind
  before optimizing history that others have already applied.
