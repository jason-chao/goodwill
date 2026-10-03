# Configuration

There are two kinds of settings: **server settings**, in a file or environment variables, and **site settings**, stored in the database and changed from the command line.

## Server settings

Pass a TOML file with `-config FILE` or the `GOODWILL_CONFIG` environment variable. Every setting is optional and can also be given as an environment variable, which takes precedence over the file. [examples/goodwill.example.toml](../examples/goodwill.example.toml) is a commented starting point.

| Setting | Environment variable | Default | Meaning |
|---|---|---|---|
| `server.public_listen` | `GOODWILL_PUBLIC_LISTEN` | `127.0.0.1:8080` | Address for the tracker script and event endpoints |
| `server.admin_listen` | `GOODWILL_ADMIN_LISTEN` | `127.0.0.1:8081` | Address for the dashboard |
| `server.trusted_proxies` | `GOODWILL_TRUSTED_PROXIES` | none | Addresses or CIDR ranges of reverse proxies whose client-address header is believed. Comma-separated in the environment variable |
| `server.client_ip_header` | `GOODWILL_CLIENT_IP_HEADER` | `X-Forwarded-For` | The header a trusted proxy puts the visitor's address in |
| `server.public_url` | `GOODWILL_PUBLIC_URL` | none | Public address of the server, shown in the tracking snippet |
| `server.secure_cookies` | `GOODWILL_SECURE_COOKIES` | `false` | Mark the dashboard session cookie HTTPS-only |
| `database.path` | `GOODWILL_DATABASE_PATH` | `goodwill.db` | The SQLite database file |
| `geo.path` | `GOODWILL_GEO_PATH` | none | A MaxMind DB (`.mmdb`) file to use for location lookups |
| `geo.attribution` | `GOODWILL_GEO_ATTRIBUTION` | none | Credit shown in the dashboard footer when `geo.path` is set |
| `limits.events_per_minute` | `GOODWILL_EVENTS_PER_MINUTE` | `600` | Events accepted per minute from one address; `0` turns the limit off |
| `limits.max_body_bytes` | `GOODWILL_MAX_BODY_BYTES` | `65536` | Largest accepted event request |

A misspelt setting in the file is an error rather than being ignored.

### Location

Location comes from looking up the visitor's address in a database file. The address itself is not stored.

- **Built in:** a binary made with `make build` contains the [DB-IP](https://db-ip.com) IP to Country Lite database (CC BY 4.0), which gives the country. It is as recent as the build; rebuild to refresh it.
- **Your own file:** set `geo.path` to any MaxMind DB file. This replaces the built-in database. A city-level file, such as MaxMind's GeoLite2 City or DB-IP's City Lite, also enables regions and cities for sites that ask for them. You download and update the file yourself; MaxMind's requires a free account. If its licence asks for a credit, put the text in `geo.attribution`.
- **None:** a binary made with `make build-nogeo` and no `geo.path` records no location.

## Site settings

```sh
goodwill site add --name NAME [--domains a.com,b.com] [--timezone ZONE] [--salt-interval day]
goodwill site list
goodwill site set ID [options]
goodwill site key ID
```

`site set` changes only the options you give:

| Option | Values | Default | Meaning |
|---|---|---|---|
| `--name` | text | | Display name |
| `--domains` | hostnames, or `any` | any | Hostnames allowed to send events. `example.com` also covers `www.example.com`; `*.example.com` covers every subdomain |
| `--timezone` | an IANA zone such as `Europe/London` | `UTC` | Decides where days and hours begin. Applies to events recorded from now on; earlier events keep the day they were filed under |
| `--salt-interval` | `day`, `week`, `month` or `<n>d` | `day` | How long a visitor can be recognised. See [Privacy](privacy.md#how-visitors-are-counted) |
| `--location` | `none`, `country`, `region`, `city` | `country` | How much location detail to keep. Region and city need a city-level database |
| `--ip-mode` | `full`, `masked` | `full` | `masked` hashes a shortened address and ignores the browser, which is more private and less precise |
| `--ignore-ips` | addresses or CIDR ranges, or `none` | none | Events from these addresses are discarded, for example your own office |
| `--trusted-sources` | addresses or CIDR ranges, or `none` | none | Your own servers, which may send events on behalf of visitors. See [Tracking](tracking.md#events-from-your-server) |
| `--keep-query` | query-string keys, or `none` | none | Keys kept as part of the page path. Everything else in the query string is dropped |
| `--collect` | `name=on` or `name=off`, comma-separated | see below | Switch individual data points on or off |

The `--collect` switches:

| Name | Default | What it controls |
|---|---|---|
| `referrer` | on | The site and page a visitor came from |
| `user_agent` | on | Browser, operating system and device type |
| `screen` | on | Screen width, as one of six size classes |
| `language` | on | Browser language |
| `utm` | on | `utm_source`, `utm_medium`, `utm_campaign`, `utm_content`, `utm_term` |
| `props` | on | Properties sent with custom events |
| `identify` | **off** | The ID passed to `umami.identify()` |
| `performance` | on | Page speed measurements, when the tracker sends them |

For example:

```sh
goodwill site set 1 --salt-interval week --location city --collect identify=on,screen=off
```

A running server picks changes up within 15 seconds. A new salt interval takes effect when the current one ends.

## Dashboard users

```sh
goodwill user add USERNAME
```

This creates the user, or sets a new password if it already exists. Passwords must be at least 10 characters. To supply the password from a script, pipe it in and add `-password-stdin`.
