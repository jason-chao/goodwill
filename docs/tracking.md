# Tracking pages and events

goodwill serves the [Umami](https://umami.is) tracker script and accepts its event format. If you have used that tracker before, everything on this page will be familiar, and [the last section](#differences-from-an-umami-server) lists what is different.

## Page views

Add the snippet to every page, just before `</head>`:

```html
<script defer src="https://stats.example.com/script.js" data-website-id="YOUR-WEBSITE-ID"></script>
```

`goodwill site add` prints the snippet with your website ID, and the dashboard's Setup page shows it again. Page views are recorded automatically, including navigation inside single-page apps.

Useful attributes on the script tag:

| Attribute | Effect |
|---|---|
| `data-domains="example.com,www.example.com"` | Only track on these hostnames, so local development is not counted |
| `data-auto-track="false"` | Do not record page views automatically; call `umami.track()` yourself |
| `data-do-not-track="true"` | Respect the browser's Do Not Track setting |
| `data-exclude-search="true"` | Leave the query string out of what is sent |
| `data-tag="variant-b"` | Label every event from this page, for example to compare two versions |
| `data-performance="true"` | Also send page speed measurements |
| `data-host-url="https://stats.example.com"` | Send events to a different address from the one the script was loaded from |

A visitor can opt out in their own browser by running `localStorage.setItem('umami.disabled', '1')`.

## Custom events

With HTML attributes, no JavaScript needed. Every `data-umami-event-*` attribute becomes a property:

```html
<button data-umami-event="signup" data-umami-event-plan="pro">Sign up</button>
```

From your own code, which lets properties be numbers and booleans as well as text:

```js
umami.track('signup', { plan: 'pro', seats: 3, trial: true });
umami.track('purchase', { revenue: 29.50, currency: 'USD' });
```

Rules for events and properties:

- An event name is cut to 50 characters, and is refused if it starts with `=`, `+`, `-`, `@`, a tab or a carriage return.
- An event carries at most 30 properties. Extra ones are dropped.
- Property names are at most 64 characters. Text values are cut to 255 characters.
- Numbers and booleans keep their type, so the dashboard can total and average numeric properties.
- Nested objects are flattened: `{ billing: { cycle: 'monthly' } }` is stored as `billing.cycle`.
- A property that cannot be stored is dropped on its own. The rest of the event is kept.

In the dashboard, the Events card lists each event; choose **Properties** beside one to see its properties, their most common values, and totals for numeric ones.

## Identifying signed-in users

```js
umami.identify('user-123', { plan: 'pro' });
```

This is **off by default**: the ID is discarded. To keep it, turn it on for the site:

```sh
goodwill site set ID --collect identify=on
```

Only do this for apps where your users expect to be recognised. It links activity to a person, which the rest of goodwill is designed to avoid.

## Events from your server

Anything that can send an HTTP request can record an event:

```sh
curl -X POST https://stats.example.com/api/send \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer YOUR-INGEST-KEY' \
  -d '{"type":"event","payload":{"website":"YOUR-WEBSITE-ID","hostname":"example.com","url":"/checkout","name":"order","data":{"total":42.5}}}'
```

A server usually reports something a visitor did. To count it against that visitor rather than against your server, pass the visitor's address and user agent along in the payload as `ip` and `userAgent`. You may also pass `timestamp` (Unix seconds) for an event that happened a little earlier.

Because these three fields let a sender speak for someone else, goodwill only honours them from a **trusted sender**. There are two ways to be one:

- **An ingest key.** `goodwill site key ID` prints a key, once. Send it as `Authorization: Bearer <key>`. Issuing a new key cancels the old one.
- **A trusted address.** `goodwill site set ID --trusted-sources 192.0.2.10` trusts everything from that address. Use this for client libraries that cannot send an extra header, such as `@umami/node`.

From anyone else, `ip`, `userAgent` and `timestamp` are ignored and the event is attributed to whoever sent it.

A trusted sender can also post up to 500 events at once to `/api/batch`, as a JSON array of the same `{type, payload}` objects.

A `timestamp` cannot be older than the start of the current [salt interval](privacy.md#how-visitors-are-counted) (the start of today, by default): the salt needed to count the visitor consistently no longer exists.

## Allowed domains

`goodwill site set ID --domains example.com` restricts a site to events from those hostnames. For requests from browsers, goodwill checks the `Origin` header, which a web page cannot forge.

This keeps other websites' traffic out of your numbers. It does not stop someone determined: a website ID is public, and a script outside a browser can claim any hostname. No analytics tool that accepts events from browsers can fully prevent that.

## What gets dropped

- Requests from known crawlers, scripts and other automated clients.
- Requests from addresses in the site's `--ignore-ips` list.
- More than `limits.events_per_minute` events from one address.

Crawlers and ignored addresses receive an ordinary success response, so they learn nothing from it.

## Differences from an Umami server

goodwill accepts the same requests, with these deliberate differences:

| | goodwill |
|---|---|
| Response to `/api/send` | `{"ok": true}`. No session ID, visit ID or cache token is returned to the browser |
| `ip`, `userAgent`, `timestamp` in the payload | Honoured only from a trusted sender |
| `/api/batch` | Requires a trusted sender |
| `identify` | Off unless switched on for the site |
| Screen size | Stored as a size class, not exact pixels |
| Query strings | Dropped, apart from UTM parameters and any keys the site chooses to keep |
| Click IDs such as `gclid` and `fbclid` | Not stored |
| Link and pixel tracking, session replay | Not supported |
