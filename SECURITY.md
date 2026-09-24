# Security policy

## Supported versions

Until v1.0.0, only the latest minor version receives fixes.

## Reporting a vulnerability

Report privately through GitHub's [private vulnerability
reporting](https://github.com/ctolon/gormgate/security/advisories/new).
Please do not open a public issue for a vulnerability.

Include what you have: the affected version, the database backend, a schema
or migration that triggers it, and what an attacker gains. A proof of
concept helps but is not required.

You can expect an acknowledgement within a week and an assessment within
two.

## What is in scope

gormgate generates and runs DDL. The interesting failure modes are:

- **SQL injection through a migration's inputs.** Identifiers (table,
  column, index and constraint names) and literal defaults reach generated
  SQL. They are quoted by the backend's `QuoteName` and `QuoteValue`, which
  are the ones gorm uses for the same dialect. A name or default that
  escapes its quoting is a vulnerability.
- **Running SQL on the wrong database.** Database routers decide what may be
  migrated where; a router being bypassed is a vulnerability.
- **Reading or writing outside the project.** `makemigrations` writes files
  and `inspectdb` reads a schema; a path escaping the configured migrations
  directory is a vulnerability.

## What is not in scope

- `RunSQL` executes the SQL a migration author wrote. That is its purpose.
- A migration author can already run arbitrary code through `RunGo`; the
  migrations of a project are trusted code, like the rest of it.
- Anything reachable only by someone who can already edit the project's Go
  source or its database credentials.
