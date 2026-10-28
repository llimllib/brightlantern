#!/usr/bin/env bash
#
# Starts brightlantern over the fixture sessions, for Playwright.
#
#   e2e/serve.sh PORT          the committed fixtures, never changing
#   e2e/serve.sh PORT DIR      a copy of them in DIR, watched
#
# Playwright's webServer waits for the port, so this has to end in the server
# and not in a subshell. It builds its own index rather than touching the real
# one: the tests assert which session is newest and how many rows a filter
# leaves, and neither survives contact with someone's actual corpus.
set -euo pipefail

port="${1:-18123}"
watched="${2:-}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

# A fresh index per run, in a temp directory, so nothing accumulates and a
# failed run cannot poison the next one.
tmp="$(mktemp -d)"

# app.js is generated and the web package embeds it, so a stale build would
# mean testing the previous version of the keyboard handling.
pnpm exec tsc

# Into the temp directory, not the repository: Playwright starts one server
# per mode, and the second build would otherwise replace the binary the first
# is running.
go build -o "$tmp/brightlantern" ./cmd/brightlantern

db="$tmp/e2e.db"
if [ -z "$watched" ]; then
  "$tmp/brightlantern" index --db "$db" --dir e2e/sessions --lexical --no-titles >/dev/null
  # --no-watch: the fixtures do not change while the suite runs, and the
  # watcher would hold the write lock for no reason. --lexical, in both
  # modes: the tests count the rows a search leaves, and the semantic ranker
  # returns neighbours with no threshold. On a machine with a GPU every
  # fixture matched "flexbox" once the model loaded, so a result depended on
  # how long that took.
  exec "$tmp/brightlantern" serve --db "$db" --dir e2e/sessions --addr "127.0.0.1:$port" --no-watch --lexical
fi

# Watched, the tests write sessions into DIR and expect them to appear. A copy,
# because a session written into the committed fixtures would be indexed by
# every later run, and the index keeps a session whose file is gone, so not
# even deleting it afterwards would take it back out of this server.
rm -rf "$watched"
mkdir -p "$watched"
cp -R e2e/sessions/. "$watched"

# Older than every fixture, and enough of them that the list scrolls: holding
# the rows still when a session arrives is only observable in a list that can
# move.
filler="$watched/--Users-e2e-code-filler--"
mkdir -p "$filler"
for i in $(seq -w 1 20); do
  at="2026-02-01T10:00:${i}.000Z"
  printf '%s\n%s\n' \
    "{\"type\": \"session\", \"version\": 3, \"id\": \"e2e-filler-$i\", \"timestamp\": \"$at\", \"cwd\": \"/Users/e2e/code/filler\"}" \
    "{\"type\": \"message\", \"timestamp\": \"$at\", \"message\": {\"role\": \"user\", \"content\": [{\"type\": \"text\", \"text\": \"filler session $i\"}]}}" \
    >"$filler/e2e-filler-$i.jsonl"
done
"$tmp/brightlantern" index --db "$db" --dir "$watched" --lexical --no-titles >/dev/null
exec "$tmp/brightlantern" serve --db "$db" --dir "$watched" --addr "127.0.0.1:$port" --no-titles --lexical
