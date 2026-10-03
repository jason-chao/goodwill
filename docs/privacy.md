# What is stored, and what is not

This page describes what goodwill does with the data it receives. It is a description of the software, not legal advice: whether you need to tell your visitors or ask for their consent depends on where you and they are, and on what else your site does.

## Never stored

- **IP addresses.** An address is used while a request is being handled, to look up a country and to compute a visitor hash, and is then discarded. It is not written to the database or to goodwill's log.
- **User agent strings.** Reduced to a browser name, an operating system name and a device type; the string itself is discarded.
- **Cookies or anything else on the visitor's device.** goodwill sets none and reads none.
- **Exact screen sizes.** Reduced to one of six width classes.
- **Query strings.** Dropped, apart from UTM campaign parameters and any keys you explicitly choose to keep, because they often carry tokens, search terms and email addresses.

One rate limiter holds addresses in memory, for at most a minute, to count requests. It is never written out.

A test, `TestNoRawIdentifiersOnDisk`, sends traffic and then reads the database files back byte by byte to check that no address or user agent string is in them.

## Stored with each event

| | Example | Can be switched off |
|---|---|---|
| Time | | |
| Page host, path and title | `example.com`, `/pricing`, `Pricing` | |
| Event name and properties, for custom events | `signup`, `{"plan": "pro"}` | properties: yes |
| Referring site and page | `news.example.org`, `/item` | yes |
| Channel | `search` | follows referrer |
| UTM campaign parameters | `newsletter` | yes |
| Browser, operating system, device type | `Firefox`, `Linux`, `desktop` | yes |
| Screen width class | `lg` | yes |
| Browser language | `en-GB` | yes |
| Country, and region and city if you ask for them | `GB` | yes |
| A visitor hash and a visit ID | 8 bytes each | |
| The ID from `identify()` | `user-123` | **off unless you turn it on** |

The switches are per site: see [Configuration](configuration.md#site-settings).

## How visitors are counted

To count visitors without cookies, goodwill computes a hash for each event:

    HMAC-SHA256(salt, site, address, user agent), cut to 8 bytes

Two events with the same hash are treated as the same visitor. What makes this private is the **salt**:

- It is 32 random bytes, generated on the server, different for every site.
- It is **replaced at the end of each interval**: every day at midnight in the site's timezone, by default.
- The old salt is **overwritten, not archived.** Once it is gone, nobody, including you, can recompute an old hash from an address, or tell whether a hash from Tuesday and one from Wednesday are the same person.

Within one interval the hash is a pseudonym: stable, and in principle someone holding both the database and the current salt could test a guessed address against it. After the interval ends, that is no longer possible.

### Choosing the interval

`goodwill site set ID --salt-interval day|week|month|<n>d`

| Interval | You can count | Trade-off |
|---|---|---|
| `day` (default) | Unique visitors per day | A visitor who returns on another day is counted again. Totals over longer periods are the sum of daily visitors |
| `week`, `month`, `<n>d` | Unique visitors across that many days | Hashes stay linkable for longer |

The dashboard says which applies whenever the period you are looking at is longer than the interval.

A change takes effect when the current interval ends. `<n>d` intervals are counted in whole days from 1 January 1970, so their boundaries do not depend on when you set them.

### Masked addresses

`goodwill site set ID --ip-mode masked` hashes only the first three-quarters of an IPv4 address (or the first 48 bits of IPv6) and leaves the user agent out. Everyone on the same network then counts as one visitor. This undercounts, and makes the hash say even less about any one person.

### Visits

A visit is a run of events from one visitor with no gap longer than 30 minutes. Open visits are kept in memory, keyed by visitor hash. On shutdown they are saved to the database, as hashes and visit IDs only, so that a restart does not split them.

## Things goodwill cannot do for you

- **Your reverse proxy sees every address.** If it keeps access logs, turn them off for the analytics site or strip the addresses.
- **Page paths and titles are stored as sent.** If your URLs contain names, email addresses or tokens in the *path*, those will be stored. Query strings are dropped, but paths are not rewritten.
- **Event properties are stored as sent.** Do not put personal data in them.
- **`identify()` is not anonymous.** Turning it on links events to the ID you pass.

## Outbound connections

The server makes none. It does not check for updates and has no telemetry. The only download is at build time, when `make build` fetches the country database.
