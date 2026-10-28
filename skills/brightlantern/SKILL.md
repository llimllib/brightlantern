---
name: brightlantern
description: Search past coding agent sessions (pi and Claude Code) with Bright Lantern. Use when asked what was found, decided, or tried before, or to look for earlier work on a bug, ticket, tool, or error.
---

# Bright Lantern

Search sessions, then read the part that matched.

```bash
brightlantern search xdist                 # best matches first
brightlantern search "pytest xdist" --limit 20
brightlantern search '"fixture bugs"'      # quoted: exact phrase only
brightlantern search xdist --sort new      # newest first

brightlantern show ID --around MSG         # messages around a search hit
brightlantern show ID --from -20           # how the session ended
brightlantern show ID --from MSG --to MSG+1 --tools full   # one message, full tool output
```

Each result's "best match at message N" is the MSG to pass to `--around`.

- Search for specific words: identifiers, error text, ticket numbers.
- A result from today quoting your own question is the current session; skip it.
- Cite the session id and date when reporting a finding.
