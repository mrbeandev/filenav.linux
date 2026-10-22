#!/bin/sh
# filenav installer: downloads the latest release binary for this machine.
#   curl -fsSL https://raw.githubusercontent.com/mrbeandev/filenav.linux/main/scripts/install.sh | sh
# Then run `filenav setup` (or `sudo filenav setup` for a system-wide service).
set -eu

REPO="mrbeandev/filenav.linux"
VERSION="${FILENAV_VERSION:-latest}"

case "$(uname -s)" in
  Linux) os=linux ;;
  *) echo "filenav release binaries are Linux-only (got $(uname -s)). Build from source: go build ." >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  armv7l|armv7*) arch=armv7 ;;
  i386|i686) arch=386 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

asset="filenav-${os}-${arch}"
if [ "$VERSION" = "latest" ]; then
  base="https://github.com/${REPO}/releases/latest/download"
else
  base="https://github.com/${REPO}/releases/download/${VERSION}"
fi

if [ "$(id -u)" = "0" ]; then dest="/usr/local/bin"; else dest="${HOME}/.local/bin"; fi
mkdir -p "$dest"
tmp="$(mktemp)"
trap 'rm -f "$tmp" "$tmp.sums"' EXIT

echo "Downloading ${asset} (${VERSION})..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL -o "$tmp" "${base}/${asset}"
  curl -fsSL -o "$tmp.sums" "${base}/SHA256SUMS" || true
else
  wget -qO "$tmp" "${base}/${asset}"
  wget -qO "$tmp.sums" "${base}/SHA256SUMS" || true
fi

if [ -s "$tmp.sums" ] && command -v sha256sum >/dev/null 2>&1; then
  want="$(grep " ${asset}\$" "$tmp.sums" | cut -d' ' -f1)"
  got="$(sha256sum "$tmp" | cut -d' ' -f1)"
  if [ -n "$want" ] && [ "$want" != "$got" ]; then
    echo "checksum mismatch for ${asset}!" >&2; exit 1
  fi
  echo "Checksum OK"
fi

chmod +x "$tmp"
mv "$tmp" "${dest}/filenav"
trap - EXIT
echo "Installed ${dest}/filenav ($("${dest}/filenav" version))"
case ":$PATH:" in *":${dest}:"*) ;; *) echo "Note: ${dest} is not in your PATH" ;; esac
echo
echo "Next steps:"
echo "  filenav            # run it now (creates ~/.filenav_linux/config.yml)"
echo "  filenav setup      # install as a service that starts at boot (use sudo for system-wide)"
