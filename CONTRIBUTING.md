# Contributing to gormgate

Thanks for taking the time. gormgate is a port of Django 6.0's migration
framework, and that shapes how it is developed: most decisions have already
been made in Django, and our job is to reproduce them faithfully in Go.

## The one rule

**What a command decides is Django's; what it prints is ours.** Given the
same models, gormgate must write the same migrations, with the same
operations, in the same order, and exit with the same status. A live parity
harness enforces exactly that: it runs the same sequences against a real
Django project in Docker and an equivalent gormgate project and compares
what each of them did.

The wording is gormgate's own and is free to change. Options are spelled
the way Go command line tools spell them, and
[`docs/from-django.md`](docs/from-django.md) maps them onto Django's.

The same applies to generated SQL: it must be byte-identical to what
`gorm.AutoMigrate` produces for the same models, which `TestGormParity`
enforces for every supported database.

## Porting a function

Every function ported from Django carries a provenance comment:

```go
// generateRenamedFields detects fields renamed within a model.
//
// django: autodetector.py MigrationAutodetector.generate_renamed_fields
func (a *Autodetector) generateRenamedFields() error {
```

These comments are load-bearing: they are how a reviewer checks the port
against the original. Keep them accurate, and add one when you port
something new.

Only the algorithm core carries them: the project state, the graph, the
loader, the autodetector, the optimizer and the executor, where the
correctness comes from Django. The command layer and the backends are
ordinary Go and carry no provenance comments.

The Django source is easy to read from the parity image:

```console
$ docker run --rm gormgate-parity:django6 python -c \
    "import inspect; from django.db.migrations import autodetector; \
     print(inspect.getsource(autodetector.MigrationAutodetector.generate_renamed_fields))"
```

## Error text

The text of a Go `error` is a Go error string: lower case, no trailing
period. So is what a command prints: lower case, no full stop, saying what
happened rather than announcing what is about to.

## Running the tests

Unit tests need nothing but Go:

```console
$ go test ./...
```

Everything else needs Docker. The databases live in `itest/compose.yaml`:

```console
$ make -C itest up                      # start every database, wait for health
$ make -C itest smoke                   # reachability check for every vendor
$ cd itest && GORMGATE_ITEST_REQUIRE=1 GORMGATE_ITEST_VENDORS=pg18 \
    go test -tags integration -run TestConformance -count=1 .
```

`GORMGATE_ITEST_VENDORS` takes a comma-separated list of vendor keys:
`pg14 pg18 sqlite sqlite-purego mysql80 mysql84 mariadb106 mariadb118 tidb
mssql cockroach gauss clickhouse`. Oracle needs Oracle Instant Client and so
runs inside a container: `make -C itest oracle-test`.

`GORMGATE_ITEST_REQUIRE=1` turns "database unreachable" from a skip into a
failure. Use it, or a typo in a vendor key will look like a pass.

The CLI parity suite needs the Django image, which it builds itself:

```console
$ cd itest && GORMGATE_ITEST_REQUIRE=1 \
    go test -tags 'parity integration' -run TestDjangoParity -count=1 -v .
```

## The parity harness

`itest/parity_test.go` (build tags `parity integration`) is the one that
runs real Django. It builds the image from `parity/Dockerfile`, which pins
the Python, Django and psycopg versions, and runs both projects against the
same PostgreSQL so that a schema comparison means something.

It compares what the two sides did, not what they said: the exit status of
every step, the migrations each project has on disk, and the output of
`migrate --plan` reduced to the migrations and operations it lists.
`djangoArgv` translates the options Django spells differently, and is the
executable half of the table in `docs/from-django.md`.

When a scenario fails, read it as a real difference in behaviour. Wording
is not compared, so a failure is never cosmetic.

## Documentation

The site is MkDocs Material; `make docs` builds it and `make docs-serve`
serves it. Two things in it are generated rather than written, so that they
cannot drift from the code:

- **The usage block of every page under `docs/commands/`** comes from the
  command's real `--help`. Regenerate with `scripts/gen-cli-docs.sh`; CI
  runs it with `--check` and fails if a page is out of date. The prose
  around the block is hand-written and preserved.
- **The Go examples in the guide** are included from compiled, tested source
  — `doc_snippets_test.go` and `example_test.go` at the repository root —
  with mkdocs' snippets extension:

  ````markdown
  ```go
  --8<-- "doc_snippets_test.go:datamigration"
  ```
  ````

  Add the code there first, behind `// --8<-- [start:name]` markers, then
  include it. Do not paste Go into a page: a pasted example is not compiled,
  and renaming a section that a page includes fails the docs build, which is
  the point.

Console output in the documentation is real output, captured by running the
example project against a database. If you change what a command prints,
re-run it rather than editing the block by hand.

## Before you open a pull request

```console
$ gofmt -l . itest parity/goproj examples/blogproj   # must print nothing
$ go vet ./...
$ staticcheck ./...                                  # configured by staticcheck.conf
$ go test ./...
```

`staticcheck.conf` enables every check. If one fires on code you believe is
right, fix the code or add a `//lint:ignore` with a reason a reader can
check — do not weaken the configuration.

A change to a backend must be run against that backend's database, and a
change to `backends/base` against all of them. Say in the pull request which
vendors you ran.

## Adding a database backend

`backends/base` holds the shared algorithms; a backend supplies the pieces
that differ: a `base.Grammar` saying how it spells each statement, the
`base.Overrides` steps it replaces, and a `base.Detector` registering it.
Start from the smallest existing backend (`backends/tidb`, which only
overrides what TiDB does differently from MySQL) and read
[`docs/vendors.md`](docs/vendors.md) for what each existing backend had to
handle.

A new backend is not finished until `TestConformance`, `TestGormParity` and
`TestPropertyEvolution` pass against a real server of that database, its
service is in `itest/compose.yaml`, and its section is in `docs/vendors.md`.
A feature flag must state what the server actually does — verified against
the server, not guessed from documentation.

## Cutting a release

The version is read from the module's build information, so no source file
names it and nothing has to be edited to cut one.

1. Move the entries under `## [Unreleased]` in `CHANGELOG.md` into a new
   `## [x.y.z] - YYYY-MM-DD` section, and add its link reference at the
   bottom of the file. The release notes are taken from that section
   verbatim, and the workflow fails if it is missing.
2. Push the tag: `git tag -a vx.y.z -m vx.y.z && git push origin vx.y.z`.

Pushing the tag runs the whole of CI on it — every database, Oracle, the
end-to-end suite and the Django parity suite — and, separately,
`.github/workflows/release.yml`, which re-checks the module path against
the tag's major version, re-runs the gates that need no database, publishes
the release with those notes, and asks the module proxy to fetch it so that
pkg.go.dev picks it up.

A tag with a suffix (`v1.0.0-rc.1`) is published as a pre-release.

## What is deliberately not supported

[`docs/limitations.md`](docs/limitations.md) is the single list of what
gormgate knowingly does not do: missing commands, deliberate limits of the
port, known debts in the code, and what has not been exercised yet. When
you decide not to do something, add it there rather than leaving the
reasoning in a comment.

Two lists feed into it. [`docs/not-applicable.md`](docs/not-applicable.md) has the Django
concepts that have no meaning in GORM, and the Django tests that were
therefore not ported, each with its reason;
[`docs/from-django.md`](docs/from-django.md) gives the gorm equivalent, or
the recipe, for each one a user will look for.
