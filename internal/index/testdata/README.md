# v0.0.1.db

An index written by the released v0.0.1 binary, committed so that migrations
are tested against a real old database rather than one this code generated
and then rolled back (#66). Never regenerate it with a newer build: being
v0.0.1's output is the only thing it is for.

Made with:

```sh
git worktree add /tmp/v001 v0.0.1 && cd /tmp/v001
mise run sqlite-header && mise run ts && go build -o /tmp/spireweb-v001 ./cmd/spireweb
# a stand-in for the Messages API answering "Fixture session title N"
ANTHROPIC_API_KEY=fake ANTHROPIC_BASE_URL=http://127.0.0.1:18765 \
  /tmp/spireweb-v001 index --db v0.0.1.db --dir e2e/sessions --titles-via api
sqlite3 v0.0.1.db 'PRAGMA wal_checkpoint(TRUNCATE)'
```

The three e2e sessions: 48 archived messages, 46 chunks, and three titles
with their `title_key` and `title_msgs`. Titles are in it because a reset's
cost is mostly re-titling, and a fixture without them could not show they
survived.

No `chunks_vec`: it was built on a machine where the model could not load.
The vector invariants need the model to produce vectors, not to check them,
so a fixture with vectors would be worth making on a machine with a GPU.
