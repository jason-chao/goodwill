# Contributing

Bug reports and pull requests are welcome.

## Working on the code

```sh
make test     # run the tests
make check    # everything CI runs
make build    # build ./goodwill with the country database
```

The layout:

| Path | Contents |
|---|---|
| `cmd/goodwill` | The command line |
| `internal/ingest` | The public endpoints that receive events |
| `internal/identity` | Salts, visitor hashes and visits |
| `internal/enrich` | Turning a request into the descriptors that are stored |
| `internal/store` | The database: schema, writes and queries |
| `internal/dashboard`, `web/` | The dashboard and its templates and static files |
| `tracker/` | The tracking script, built from a pinned Umami release |

## Ground rules

- **Raw addresses and user agents never reach the disk or the log.** `TestNoRawIdentifiersOnDisk` checks the database files; keep it passing, and extend it when you add a new way for data to arrive.
- **Tests come with the change.** Numbers shown on the dashboard are checked against a small dataset counted by hand in `internal/store/query_test.go`.
- **Keep it small.** A new dependency or a new background process needs a good reason.
- **Keep examples generic.** Use `example.com` and the address ranges reserved for documentation (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`, `2001:db8::/32`). Do not commit hostnames, addresses or settings from a real installation.

## Updating the tracker

`tracker/script.js` is built, unmodified, from a tagged Umami release:

```sh
UMAMI_TAG=v3.4.0 ./scripts/build-tracker.sh
```

This rewrites `tracker/script.js` and `tracker/VERSION`. A test checks that the two agree. When moving to a new release, read its changes to the tracker and to the `/api/send` format, and update `internal/ingest` and its tests to match.
