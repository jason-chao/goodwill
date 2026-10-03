#!/bin/sh
# Rebuilds tracker/script.js from a pinned Umami release.
#
# The tracker is the MIT-licensed browser script from the Umami project. It is
# built unmodified, with the collection endpoint set to /api/send. Only the
# built file is committed; tracker/VERSION records where it came from.
#
# Requires: git, node (>= 20), npm, sha256sum.
set -eu

TAG="${UMAMI_TAG:-v3.4.0}"
REPO="https://github.com/umami-software/umami.git"

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

git clone -q --depth 1 --branch "$TAG" "$REPO" "$WORK/umami"
COMMIT=$(git -C "$WORK/umami" rev-parse HEAD)

# Build in a clean directory holding only the tracker sources, so that just
# the tracker toolchain is installed rather than the whole application.
mkdir -p "$WORK/build/src"
cp -r "$WORK/umami/src/tracker" "$WORK/build/src/tracker"
cp "$WORK/umami/tsconfig.json" "$WORK/umami/tsconfig.tracker.json" \
   "$WORK/umami/rollup.tracker.config.js" "$WORK/build/"
printf '{"private":true,"type":"module"}\n' > "$WORK/build/package.json"

# Toolchain versions come from the release's own package.json.
DEPS=$(node -e '
  const p = require(process.argv[1]);
  const all = { ...p.dependencies, ...p.devDependencies };
  const want = ["rollup", "@rollup/plugin-replace", "@rollup/plugin-terser",
                "@rollup/plugin-typescript", "typescript", "tslib", "dotenv"];
  // Anything the release does not list directly (tslib) is taken at its latest version.
  console.log(want.map(n => all[n] ? n + "@" + all[n].replace(/^[\^~]/, "") : n).join(" "));
' "$WORK/umami/package.json")

cd "$WORK/build"
# shellcheck disable=SC2086
npm install --silent --no-audit --no-fund $DEPS
COLLECT_API_HOST="" COLLECT_API_ENDPOINT="/api/send" npx rollup -c rollup.tracker.config.js

mkdir -p "$ROOT/tracker"
cp public/script.js "$ROOT/tracker/script.js"
cp "$WORK/umami/LICENSE" "$ROOT/tracker/LICENSE"

SUM=$(sha256sum "$ROOT/tracker/script.js" | cut -d' ' -f1)
cat > "$ROOT/tracker/VERSION" <<EOF
source: $REPO
tag: $TAG
commit: $COMMIT
endpoint: /api/send
sha256: $SUM
EOF

echo "built tracker/script.js from $TAG ($COMMIT)"
echo "sha256 $SUM"
