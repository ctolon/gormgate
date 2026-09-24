#!/usr/bin/env bash
# Regenerates the "usage" block of every page under docs/commands/ from the
# command's real --help output, so the reference cannot drift from the code.
#
# The prose in each page is hand-written and preserved; only the text between
# the BEGIN/END markers is replaced.
#
#   scripts/gen-cli-docs.sh          regenerate
#   scripts/gen-cli-docs.sh --check  fail if regenerating would change anything
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
commands=(makemigrations migrate sqlmigrate showmigrations squashmigrations
          optimizemigration sqlsequencereset inspectdb check)

bin=$(mktemp -d)/gorm-gate
trap 'rm -rf "$(dirname "$bin")"' EXIT
(cd "$repo/examples/blogproj" && go build -buildvcs=false -o "$bin" ./cmd/gorm-gate)

status=0
for cmd in "${commands[@]}"; do
	page="$repo/docs/commands/$cmd.md"
	if [ ! -f "$page" ]; then
		echo "$page is missing; add the page, then rerun" >&2
		exit 1
	fi
	help=$(COLUMNS=80 "$bin" help "$cmd")
	new=$(awk -v help="$help" '
		/^<!-- BEGIN help -->$/ { print; print "";  print "```console"; print "$ gorm-gate help " cmd; print help; print "```"; print ""; skip = 1; next }
		/^<!-- END help -->$/   { skip = 0 }
		!skip                   { print }
	' cmd="$cmd" "$page")
	if [ "$new" = "$(cat "$page")" ]; then
		continue
	fi
	if [ "${1-}" = "--check" ]; then
		echo "docs/commands/$cmd.md is out of date; run scripts/gen-cli-docs.sh" >&2
		status=1
	else
		printf '%s\n' "$new" > "$page"
		echo "updated docs/commands/$cmd.md"
	fi
done
exit $status
