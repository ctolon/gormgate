# blogproj — the smallest complete project

Two apps (`auth`, `blog`), PostgreSQL, and a `cmd/gorm-gate` command. Start
here: it is the shape every other example is a variation on.

The whole of the wiring is
[`cmd/gorm-gate/main.go`](cmd/gorm-gate/main.go) — which apps exist, and how
to reach the database. gormgate supplies the commands.

There are no migrations committed here on purpose: generating them is the
first thing you do, and the repository's end-to-end test drives this project
to check that `makemigrations` still produces them.

## Run it

Point it at any PostgreSQL database:

```console
$ export BLOGPROJ_DSN='host=127.0.0.1 port=5432 user=you password=... dbname=blogproj sslmode=disable'
```

```console
$ go run ./cmd/gorm-gate makemigrations auth blog
created auth/migrations/0001_initial.go
  + create model User
created blog/migrations/0001_initial.go
  + create model Post
  + create model Comment

$ go run ./cmd/gorm-gate migrate
applying auth.0001_initial ... ok
applying blog.0001_initial ... ok

$ go run ./cmd/gorm-gate showmigrations
auth
 [X] 0001_initial
blog
 [X] 0001_initial
```

`makemigrations` wrote three kinds of file: the migrations themselves, a
`migrations.go` per app that names the package, and
`cmd/gorm-gate/zz_gormgate_migrations.go`, which imports every migrations
package so the command has them compiled in. All of them are source you
commit.

To see what a migration does before it does it:

```console
$ go run ./cmd/gorm-gate sqlmigrate auth 0001
BEGIN;
--
-- create model User
--
CREATE TABLE "auth_users" ("id" bigserial, "name" varchar(100) NOT NULL, "email" varchar(190), "created_at" timestamptz, PRIMARY KEY ("id"), CONSTRAINT "uni_auth_users_email" UNIQUE ("email"));
CREATE INDEX "idx_auth_users_name" ON "auth_users" ("name");
COMMIT;
```

## Starting over

```console
$ rm -rf auth/migrations blog/migrations cmd/gorm-gate/zz_gormgate_migrations.go
```

and drop the database.
