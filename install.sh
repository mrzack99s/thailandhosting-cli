#!/bin/sh
# Installs th, the ThailandHosting CLI, from GitHub Releases (Linux, macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/mrzack99s/thailandhosting-cli/main/install.sh | sh
#
# TH_VERSION=0.1.0 picks a version (default: the latest release);
# TH_INSTALL_DIR picks where th goes (default: /usr/local/bin, or
# ~/.local/bin when that is not writable). The archive is checked against
# the release's SHA256SUMS before anything is installed.
set -eu

repo="mrzack99s/thailandhosting-cli"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "th: unsupported system $(uname -s); on Windows use install.ps1" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "th: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

version="${TH_VERSION:-}"
if [ -z "$version" ]; then
  version="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1)"
fi
[ -n "$version" ] || { echo "th: could not find the latest release" >&2; exit 1; }
version="${version#v}"

archive="th_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/v$version"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading th $version ($os/$arch)…"
curl -fsSL -o "$tmp/$archive" "$base/$archive"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"

want="$(grep " $archive\$" "$tmp/SHA256SUMS" | awk '{print $1}')"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/$archive" | awk '{print $1}')"
else
  got="$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')"
fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "th: checksum mismatch for $archive" >&2; exit 1; }

tar -xzf "$tmp/$archive" -C "$tmp"
bin="$(find "$tmp" -type f -name th | head -n 1)"
[ -n "$bin" ] || { echo "th: archive has no th binary" >&2; exit 1; }

dir="${TH_INSTALL_DIR:-/usr/local/bin}"
if [ -z "${TH_INSTALL_DIR:-}" ] && [ ! -w "$dir" ]; then
  dir="$HOME/.local/bin"
fi
mkdir -p "$dir"
install -m 0755 "$bin" "$dir/th"
echo "Installed th $version to $dir/th"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH to run th." ;;
esac
echo "Next: th auth login"
