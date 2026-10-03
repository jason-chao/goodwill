create table sites (
    id              integer primary key,
    public_id       text    not null unique,
    name            text    not null,
    domains         text    not null default '[]',
    timezone        text    not null default 'UTC',
    salt_interval   text    not null default 'day',
    settings        text    not null default '{}',
    ingest_key_hash blob,
    created_at      integer not null
);

-- Exactly one salt per site. Rotation overwrites the row, so a previous
-- salt is never kept anywhere.
create table salts (
    site_id    integer primary key references sites(id) on delete cascade,
    salt       blob    not null,
    interval   text    not null,
    period     text    not null,
    rotated_at integer not null
);

create table events (
    id           integer primary key,
    site_id      integer not null,
    ts           integer not null,
    local_day    integer not null,
    local_hour   integer not null,
    kind         integer not null,
    name         text,
    visitor      blob    not null,
    visit        blob    not null,
    host         text,
    path         text,
    title        text,
    ref_host     text,
    ref_path     text,
    channel      text,
    utm_source   text,
    utm_medium   text,
    utm_campaign text,
    utm_content  text,
    utm_term     text,
    browser      text,
    os           text,
    device       text,
    screen       text,
    lang         text,
    country      text,
    region       text,
    city         text,
    tag          text,
    distinct_id  text,
    lcp          real,
    inp          real,
    cls          real,
    fcp          real,
    ttfb         real,
    props        text
);

create index events_site_ts on events (site_id, ts);

create table property_keys (
    site_id   integer not null,
    event     text    not null,
    key       text    not null,
    type      text    not null,
    last_seen integer not null,
    primary key (site_id, event, key)
) without rowid;

create table users (
    id            integer primary key,
    username      text    not null unique,
    password_hash text    not null,
    created_at    integer not null
);

create table auth_sessions (
    token_hash blob    primary key,
    user_id    integer not null references users(id) on delete cascade,
    csrf       text    not null,
    created_at integer not null,
    expires_at integer not null
) without rowid;

create table kv (
    key   text primary key,
    value blob not null
) without rowid;
