# Installing and running

## Build

You need Go 1.26 or later, `make` and `curl`.

```sh
git clone https://github.com/jason-chao/goodwill.git
cd goodwill
make build
```

This produces one static binary, `./goodwill`, with a country database built in. If you would rather supply your own location database, or none, use `make build-nogeo` (see [Location](configuration.md#location)).

## First run

```sh
./goodwill user add admin                     # asks for a password
./goodwill site add --name "My site" --domains example.com --timezone Europe/London
./goodwill serve
```

The database file (`goodwill.db` in the current directory, unless configured otherwise) is created on first use.

goodwill listens on two addresses:

| Listener | Default | Serves |
|---|---|---|
| Public | `127.0.0.1:8080` | The tracker script and the event endpoints. This is what your visitors' browsers talk to. |
| Admin | `127.0.0.1:8081` | The dashboard. |

They are separate so that the dashboard can be kept off the internet.

## Reverse proxy

Put the **public** listener behind a reverse proxy that provides HTTPS. Only four paths need to be exposed: `/script.js`, `/api/send`, `/api/batch` and `/api/heartbeat`. [examples/Caddyfile](../examples/Caddyfile) shows this with Caddy.

Two settings matter once a proxy is in front:

```toml
[server]
trusted_proxies = ["127.0.0.1"]             # the proxy's address
public_url = "https://stats.example.com"    # shown in the tracking snippet
```

Without `trusted_proxies`, every request appears to come from the proxy, and all your visitors are counted as one. goodwill warns about this at startup.

Your reverse proxy sees every visitor's address. If it writes access logs, those logs hold what goodwill is careful not to store. Turn them off for this site, or remove addresses from them.

## Reaching the dashboard

Keep the admin listener on loopback or on a private network address. Common arrangements:

- **SSH tunnel:** `ssh -L 8081:127.0.0.1:8081 your-server`, then open `http://127.0.0.1:8081`.
- **Private network:** set `admin_listen` to the server's address on a VPN or other private network, so that only devices on that network can connect.

The dashboard always asks for a username and password as well. If you serve it over HTTPS, set `secure_cookies = true`.

## Running as a service

[examples/goodwill.service](../examples/goodwill.service) is a systemd unit with sandboxing and a memory ceiling. In outline:

```sh
sudo useradd --system --home /var/lib/goodwill --shell /usr/sbin/nologin goodwill
sudo install -d -o goodwill -g goodwill -m 0750 /var/lib/goodwill
sudo install -m 0755 goodwill /usr/local/bin/goodwill
sudo install -m 0640 -g goodwill examples/goodwill.example.toml /etc/goodwill.toml   # then edit it
sudo install -m 0644 examples/goodwill.service /etc/systemd/system/goodwill.service
sudo systemctl enable --now goodwill
```

Run the other commands as the same user and with the same configuration, so that they open the same database:

```sh
sudo -u goodwill goodwill site list -config /etc/goodwill.toml
```

Sites and settings can be changed while the server is running; it picks changes up within 15 seconds.

## Backups

```sh
goodwill backup /path/to/backups/goodwill-$(date +%F).db
```

This writes a consistent copy while the server keeps running. Copy the result somewhere else. To restore, stop the server, put the copy in place of the database file, and start it again.

## Upgrading

Replace the binary and restart. Any database changes are applied automatically at startup. Take a backup first.

## Memory and disk

On a test machine goodwill used about 30 MB of memory when idle and under 40 MB while receiving 100 events a second. Each stored event took a little over 200 bytes on disk in the same test; allow 200 to 400 bytes, so a million events is a few hundred megabytes.
