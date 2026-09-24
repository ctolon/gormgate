# Squashing history

A project that has been alive for a while accumulates migrations, and every
new database replays all of them. Squashing replaces a run of migrations
with one that has the same effect.

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

## What the optimizer does

The squashed migration is not a concatenation. The optimizer folds the
operations together: a `CreateModel` followed by `AddField` becomes one
`CreateModel` with the field in it; an `AddField` and a later `RemoveField`
of the same field disappear entirely; a `RenameField` collapses into the
`CreateModel` that made it.

You can run that step on its own with
[`optimizemigration`](../commands/optimizemigration.md).

## Living with both

The squashed migration records what it `Replaces`. A database that has
already applied all of the replaced migrations is considered to have applied
the squashed one too, so both can sit in the tree while deployments catch
up. That is why the command tells you to keep the old files.

When every deployment has applied them:

1. delete the replaced migration files,
2. remove the `Replaces` field from the squashed migration,
3. run `migrate --prune <app>` to drop their rows from the migrations table.

```console
$ go run ./cmd/gorm-gate migrate --prune blog
Pruning migrations:
  Pruning blog.0001_initial OK
  Pruning blog.0002_post_views OK
  Pruning blog.0003_post_slug OK
```

## What cannot be squashed automatically

A `RunGo` or `RunSQL` has no semantics the optimizer can reason about, so it
is carried into the squashed migration as it stands. `RunGo` marked
`IsElidable: true` is dropped instead — that is what the flag is for: a
one-off backfill a fresh database will never need.

If an operation needs a human, the command says so and writes the file with
a marker rather than pretending.

## Squashing a squash

Allowed, and handled: the replacement chain is resolved recursively, so a
squash of a squash still knows which original migrations it stands for.
