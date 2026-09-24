# dockerproj — migrations as a deploy step, not as app startup

One PostgreSQL service, one image with two binaries in it (`gorm-gate` and
`server`), and a `compose.yaml` that runs the migrations **before** the app
rather than from inside it:

```console
$ docker compose run --rm migrate
$ docker compose up
```

The `migrate` service is in a Compose profile, so `docker compose up` never
starts it; `docker compose run` names it and therefore does. It runs to
completion and exits, and its exit status is the gate on the deploy.

Why it matters: an app that migrates itself at startup migrates itself once
per replica, at the same moment, against the same database. It also makes a
rollback ambiguous — the old binary comes back up against the new schema.
Splitting the two makes the schema change a thing you can run, watch and
stop.

The server does exactly one thing about the schema: it refuses to start
without it.

```go
if !db.Migrator().HasTable(&shop.Order{}) {
	return errors.New("the database is not migrated: run `docker compose run --rm migrate` first")
}
```

## What happens if you forget

```console
$ docker compose up
 Container gormgate-dockerproj-db-1 Healthy
 Container gormgate-dockerproj-app-1 Starting
 Container gormgate-dockerproj-app-1 Started
app-1  | server: the database is not migrated: run `docker compose run --rm migrate` first
app-1 exited with code 1
```

## The right order

```console
$ docker compose run --rm migrate
 Container gormgate-dockerproj-db-1 Waiting
 Container gormgate-dockerproj-db-1 Healthy
 Container gormgate-dockerproj-migrate-run-b6d522c9d374 Created
applying shop.0001_initial ... ok

$ docker compose up -d
 Container gormgate-dockerproj-db-1 Running
 Container gormgate-dockerproj-db-1 Healthy
 Container gormgate-dockerproj-app-1 Started

$ docker compose logs app
app-1  | 2026/09/23 11:50:27 listening on :8080

$ curl -s -X POST localhost:18088/orders -d '{"Item":"kettle","AmountCents":2999}'
{"ID":1,"Item":"kettle","AmountCents":2999,"PlacedAt":"2026-09-23T11:50:40.5012686Z"}

$ curl -s localhost:18088/orders
[{"ID":1,"Item":"kettle","AmountCents":2999,"PlacedAt":"2026-09-23T11:50:40.501268Z"}]
```

Any other management command goes through the same service, by overriding
its command:

```console
$ docker compose run --rm migrate gorm-gate showmigrations
shop
 [X] 0001_initial
```

Stop and throw the database away with `docker compose down -v`.

## The image

[`Dockerfile`](Dockerfile) builds both binaries into one distroless image,
from one commit, so the migration step and the app can never disagree about
the schema. `CGO_ENABLED=0` is what lets the result run on a `static` base.

Its build context is the repository root, because this example's `go.mod`
has a `replace` pointing at the gormgate checkout above it — that is why
the Dockerfile copies the library in by hand. A project that depends on a
released gormgate builds from its own directory with a plain `COPY . .`.

The database password is in `compose.yaml` because this is an example; a
deployment would inject `DOCKERPROJ_DSN` as a secret. Nothing else changes.

## Running it without Docker

```console
$ DOCKERPROJ_DSN='host=... user=... dbname=...' go run ./cmd/gorm-gate migrate
$ DOCKERPROJ_DSN='host=... user=... dbname=...' go run ./cmd/server
```
