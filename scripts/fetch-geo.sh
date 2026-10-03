#!/bin/sh
# Downloads the DB-IP "IP to Country Lite" database that "make build" embeds
# in the binary. The file is published monthly under CC BY 4.0 and is not
# kept in the repository. See NOTICE for the attribution.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
DEST="$ROOT/internal/geo/data/country.mmdb.gz"
BASE="https://download.db-ip.com/free"

# Early in a month the new file may not be out yet, so fall back one month.
THIS=$(date -u +%Y-%m)
LAST=$(date -u -d "$(date -u +%Y-%m-15) -1 month" +%Y-%m 2>/dev/null || date -u -v-1m +%Y-%m)

mkdir -p "$(dirname "$DEST")"
for MONTH in "$THIS" "$LAST"; do
    if curl -fsSL "$BASE/dbip-country-lite-$MONTH.mmdb.gz" -o "$DEST.tmp"; then
        gzip -t "$DEST.tmp"
        mv "$DEST.tmp" "$DEST"
        echo "fetched country database for $MONTH"
        exit 0
    fi
done
rm -f "$DEST.tmp"
echo "could not download the country database" >&2
exit 1
