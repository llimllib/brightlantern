#!/usr/bin/env bash
#
# Starts brightlantern over the committed fixture sessions, for Playwright.
#
# Playwright's webServer waits for the port, so this has to end in the server
# and not in a subshell. It builds its own index rather than touching the real
# one: the tests assert which session is newest and how many rows a filter
# leaves, and neither survives contact with someone's actual corpus.
set -euo pipefail

port="${1:-18123}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

# app.js is generated and the web package embeds it, so a stale build would
# mean testing the previous version of the keyboard handling.
pnpm exec tsc

go build -o brightlantern ./cmd/brightlantern

# A fresh index per run, in a temp directory, so nothing accumulates and a
# failed run cannot poison the next one.
db="$(mktemp -d)/e2e.db"
./brightlantern index --db "$db" --dir e2e/sessions --lexical --no-titles >/dev/null

# --no-watch: the fixtures do not change while the suite runs, and the watcher
# would hold the write lock for no reason.
exec ./brightlantern serve --db "$db" --dir e2e/sessions --addr "127.0.0.1:$port" --no-watch
