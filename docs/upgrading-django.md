# Upgrading to a new Django

gormgate's contract is that its command line is a specific Django's, on a
specific CPython. Both are pinned, so an upstream release never changes
gormgate's behaviour by surprise — but it does mean someone has to do the
upgrade deliberately. This is how.

## How you find out

The **upstream canary** (`.github/workflows/upstream-canary.yml`) runs the
parity suite every Monday against the latest Django 6.x and CPython 3.14.x
instead of the pinned ones. It is not a required check: when it goes red,
upstream moved.

That is the intended signal. It has already caught one: CPython changed the
wording of `invalid choice` *within* the 3.14 series, from
`(choose from 0, 1, 2)` to `(choose from '0', '1', '2')`.

## What is pinned, and where

| Pin | Where | Why |
| --- | --- | --- |
| CPython | `parity/Dockerfile` (`ARG PYTHON_VERSION`) | the interpreter the Django side runs on |
| Django | `parity/Dockerfile` (`ARG DJANGO_VERSION`) | every message, prompt and exit code is compared against it |
| psycopg | `parity/Dockerfile` (`ARG PSYCOPG_VERSION`) | both projects run on the same PostgreSQL |

The defaults are duplicated in `parityVersions` in `itest/parity_test.go`,
which is what lets the canary override them. Change both.

## The upgrade

### 1. See the damage first

Do not bump anything yet. Run the suite against the new version and read
the failures:

```console
$ cd itest
$ GORMGATE_ITEST_REQUIRE=1 \
    GORMGATE_PARITY_DJANGO=6.1.0 \
    go test -tags 'parity integration' -run TestDjangoParity -count=1 -v .
```

Every failure prints both sides, normalized. That diff is your work list.

### 2. Triage each difference

Each one is exactly one of three things, and deciding which is the whole
job:

- **Django fixed a bug or changed wording.** Follow it. gormgate's job is
  to match, not to have opinions.
- **Django changed behaviour, not just text.** Read the upstream commit,
  port the change, and keep the `// django:` comment pointing at the new
  code.
- **The difference is one gormgate already documents.** Check
  [Deviations](deviations.md) before
  assuming it is new.

A difference of your own goes in `deviations.md`, and the harness is what
keeps that list closed.

### 3. Bump the pins

`parity/Dockerfile` and `parityVersions` in `itest/parity_test.go`.

### 4. Re-record the parity fixtures if the decisions moved

The harness compares what the two sides *did*, so a change in Django's
wording costs nothing. A change in what Django decides — a new operation, a
different order, a migration it no longer writes — shows up as a failing
scenario, and that is a real difference to triage, not a fixture to
refresh.

### 5. Re-run everything

```console
$ make lint && make test
$ make -C itest up
$ cd itest && GORMGATE_ITEST_REQUIRE=1 \
    GORMGATE_ITEST_VENDORS=pg14,pg18,sqlite,sqlite-purego,mysql80,mysql84,mariadb106,mariadb118,tidb,mssql,cockroach,gauss,clickhouse \
    go test -tags integration -count=1 -timeout 90m .
$ make -C itest oracle-test
$ cd itest && GORMGATE_ITEST_REQUIRE=1 go test -tags 'parity integration' -run TestDjangoParity -count=1 -v .
```

The database suites matter even for a Django-only bump: a changed
autodetector or operation changes the SQL.

### 6. Write it down

`CHANGELOG.md` says which Django a release targets. Users choose gormgate
because it matches a Django they know, so the version it matches is part of
the release notes, not an implementation detail.

## Django's own version number

`parity/django/settings.py` and the parity project are the reference, not
gormgate's version. gormgate's own version is read from the build
information of the program that links it — there is nothing to keep in step
with a tag.
