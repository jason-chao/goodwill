# goodwill

Lightweight, privacy-friendly web analytics that you host yourself.

goodwill is a single program with a built-in database. It has no other services to run, uses about 30 MB of memory when idle, and is meant for people who run a handful of sites or apps on a small server.

- **One binary, one file of data.** The server, dashboard and SQLite database are one program. Backing up is copying a file.
- **Works with the Umami tracker.** goodwill serves the [Umami](https://umami.is) tracking script and accepts its event format, so `data-umami-event` attributes, `umami.track()` and the Umami client libraries work unchanged.
- **Private by design.** No cookies. Visitor addresses and user agents are never stored; visitors are counted with a salted hash, and the salt is replaced every day and thrown away.
- **Custom events with typed properties.** Numbers stay numbers, so they can be totalled and averaged.
- **You choose what is collected.** Referrer, browser, screen size, language, location detail and more can be switched off for each site.
- **A dashboard that stays private.** It listens on its own address, separate from the public endpoint, so it can be kept on a private network.

## Status

This is version 0.1: the core is in place and tested, and more is planned.

| In 0.1 | Planned |
|---|---|
| Page views, visitors, visits, bounce rate, visit duration | Goals and funnels |
| Pages, entry pages, referrers, channels, UTM campaigns | Page speed reports |
| Countries, browsers, operating systems, devices, screens, languages | Tracking without JavaScript |
| Custom events, with a breakdown of their properties | Reading web server logs |
| Filtering by any of the above, comparison with the previous period | Data retention and long-term roll-ups |
| Realtime view | CSV export and a read API |
| Server-side events with an ingest key | A settings page (settings are on the command line for now) |

## Quick start

You need Go 1.26 or later.

```sh
git clone https://github.com/jason-chao/goodwill.git
cd goodwill
make build            # downloads a country database and builds ./goodwill

./goodwill user add admin
./goodwill site add --name "My site" --domains example.com --timezone Europe/London
./goodwill serve
```

`site add` prints the snippet to put on your pages:

```html
<script defer src="https://stats.example.com/script.js" data-website-id="YOUR-WEBSITE-ID"></script>
```

By default the public endpoint listens on `127.0.0.1:8080` and the dashboard on `127.0.0.1:8081`. [docs/install.md](docs/install.md) covers putting it behind a reverse proxy, and running it as a service or in a container.

## Documentation

- [Installing and running](docs/install.md)
- [Configuration](docs/configuration.md)
- [Tracking pages and events](docs/tracking.md)
- [What is stored, and what is not](docs/privacy.md)

## Development

```sh
make test     # run the tests
make check    # formatting, vet, staticcheck and tests with the race detector
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

MIT. See [LICENSE](LICENSE), and [NOTICE](NOTICE) for the third-party work goodwill includes.
