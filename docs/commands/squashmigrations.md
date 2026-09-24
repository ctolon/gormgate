# squashmigrations

Replaces a run of migrations with a single one that has the same effect, so
that a long history stops being replayed from scratch on every new database.

```console
$ go run ./cmd/gorm-gate squashmigrations blog 0003
Will squash the following migrations:
 - 0001_initial
 - 0002_post_views
 - 0003_post_slug
Optimizing...
  Optimized from 4 operations to 2 operations.
Created new squashed migration .../blog/migrations/0001_squashed_0003_post_slug.go
  You should commit this migration but leave the old ones in place;
  the new migration will be used for new installs. Once you are sure
  all instances of the codebase have applied the migrations you squashed,
  you can delete them.
```

The path it prints is absolute; it is elided here.

Give a start migration to squash only part of the history:

```console
$ go run ./cmd/gorm-gate squashmigrations blog 0002 0003
```

<!-- BEGIN help -->

```console
$ gorm-gate help squashmigrations
Replace an app's migrations, from the beginning or from start, up to
and including migration, with one migration that has the same effect.

Usage:
  gorm-gate squashmigrations app [start] migration [flags]

Flags:
  -h, --help                   help for squashmigrations
      --no-header              omit the generated-by comment at the top of the file
  -y, --no-input               do not prompt; assume the answer that carries on
      --no-optimize            keep every operation instead of folding them together
      --squashed-name string   name for the new migration

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

- The squashed migration lists what it replaces, and the executor treats it
  as applied when all of the replaced migrations are. That is what lets you
  keep both in the tree during the transition.
- Operations that cannot be squashed automatically — a `RunGo` with no
  reverse, for instance — are carried over as they are, and the command
  says which ones need manual attention.
- `--no-optimize` skips the optimizer, which is useful when you want to see
  what the raw concatenation looks like.
- After every deployment has applied the old migrations, delete them and run
  `migrate --prune <app>` to drop their records.
