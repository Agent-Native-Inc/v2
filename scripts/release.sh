#!/bin/sh
# Build, sign and upload a v2 release: scripts/release.sh v0.9.0, from the repo root on a clean main
# with that tag on HEAD. It doesn't make it live: scripts/check-live.sh says how.
# Needs V2_RELEASE_BUCKET, V2_RELEASE_ACCOUNT (Cloudflare account id) and V2_SIGNING_KEY (path to the ed25519 key).
set -eu
V=${1:?usage: scripts/release.sh vX.Y.Z}
BUCKET=${V2_RELEASE_BUCKET:?set V2_RELEASE_BUCKET}
ACCOUNT=${V2_RELEASE_ACCOUNT:?set V2_RELEASE_ACCOUNT}
KEY=${V2_SIGNING_KEY:?set V2_SIGNING_KEY}
ROOT=$(pwd)
# The release's downloads. checksums.txt and checksums.txt.sig go up beside them.
FILES="v2-macos.zip v2-linux-amd64 v2-linux-arm64"

echo "$V" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || {
  echo "version must look like v0.9.0" >&2
  exit 1
}
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || {
  echo "release from main" >&2
  exit 1
}
[ -z "$(git status --porcelain)" ] || {
  echo "the tree has uncommitted changes" >&2
  exit 1
}
[ "$(git rev-parse "$V^{commit}" 2>/dev/null)" = "$(git rev-parse HEAD)" ] || {
  echo "tag $V must be on HEAD: git tag $V" >&2
  exit 1
}
[ -r "$KEY" ] || {
  echo "no signing key at $KEY" >&2
  exit 1
}

(cd web && npm ci && npm run build)
D=$(mktemp -d)
for a in arm64 amd64; do
  GOOS=darwin GOARCH=$a go build -trimpath -ldflags="-s -w -X main.version=$V" -o "$D/v2-$a" ./cmd/v2
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags="-s -w -X main.version=$V" -o "$D/v2-linux-$a" ./cmd/v2
  # This busybox has musl, not glibc, so a Linux build that isn't static fails to start here.
  [ "$(docker run --rm --platform linux/$a -v "$D:/r:ro" --entrypoint /r/v2-linux-$a busybox:1.36.1-musl --version)" = "v2 version $V" ] ||
    {
      echo "v2-linux-$a doesn't report $V (needs docker running)" >&2
      exit 1
    }
done
lipo -create -output "$D/v2" "$D/v2-arm64" "$D/v2-amd64"
[ "$("$D/v2" --version)" = "v2 version $V" ] || {
  echo "the built v2 doesn't report $V" >&2
  exit 1
}
# Old Macs and the site's install.sh look for the line "<sha256>  v2-macos.zip", so keep that file name and shasum's format.
# shellcheck disable=SC2086 # FILES is a list
(cd "$D" && ditto -c -k v2 v2-macos.zip && shasum -a 256 $FILES >checksums.txt)
go run ./scripts/sign "$KEY" "$D/checksums.txt"

# wrangler reads a wrangler.toml in the current folder; the release folder has none.
cd "$D"
for f in $FILES checksums.txt checksums.txt.sig; do
  case $f in
  *.zip) ctype=application/zip ;;
  *.txt) ctype=text/plain ;;
  *) ctype=application/octet-stream ;;
  esac
  CLOUDFLARE_ACCOUNT_ID=$ACCOUNT npx --yes wrangler r2 object put "$BUCKET/cli/$V/$f" --file "$f" --content-type "$ctype" --remote
done
echo "uploaded $BUCKET/cli/$V/ from $D"
"$ROOT/scripts/check-live.sh" "$V" || {
  echo "not live yet; once the site is deployed, run scripts/check-live.sh $V" >&2
  exit 1
}
