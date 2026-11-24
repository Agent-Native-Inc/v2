#!/bin/sh
# Says whether release vX can go live: v2.video serves the install.sh that knows Linux, and every file of vX
# downloads through v2.video (Vercel, then Convex, then R2) and matches its checksums.txt.
set -eu
V=${1:?usage: scripts/check-live.sh vX.Y.Z}
SITE=${V2_SITE:-https://v2.video}
T=$(mktemp -d)
# The release's downloads, as scripts/release.sh uploads them.
FILES="v2-macos.zip v2-linux-amd64 v2-linux-arm64"
trap 'rm -rf "$T"' EXIT

# The install.sh from before Linux runs shasum -c on all of checksums.txt, so the Linux lines fail every Mac install.
curl -fsSL "$SITE/install.sh" | grep -q v2-linux- ||
  {
    echo "$SITE/install.sh is the one from before Linux: deploy the site first" >&2
    exit 1
  }
for f in checksums.txt $FILES; do
  curl -fsSL -o "$T/$f" "$SITE/releases/$V/$f" || {
    echo "$f of $V doesn't download from $SITE" >&2
    exit 1
  }
done
(cd "$T" && shasum -a 256 -c checksums.txt >/dev/null) || {
  echo "a file of $V from $SITE doesn't match checksums.txt" >&2
  exit 1
}
echo "$V downloads and checks out on every platform. To make it live, set the server's latest version to $V."
