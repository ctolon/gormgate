## What this changes

<!-- One paragraph. If it ports something from Django, name the Django
symbol; the provenance comment in the code should name it too. -->

## Why

<!-- If this fixes a bug, what was the failure? Link the issue. -->

## Checklist

- [ ] `make lint` is clean (gofmt, go vet, staticcheck, command reference)
- [ ] `make test` passes
- [ ] A function ported from Django carries its `// django:` comment, and
      only the algorithm core carries them at all
- [ ] Error strings and command output are lower case, with no full stop

## Databases this was run against

<!-- Required for any change under backends/. A change to backends/base
needs all of them. Paste the command you ran. -->

## Does this change anything a user sees?

<!-- Command output, exit codes, generated migration files or generated SQL.
If yes, explain why that is allowed: either Django decides the same, or it
is a new entry in docs/deviations.md. Wording is ours and may change
freely. -->
