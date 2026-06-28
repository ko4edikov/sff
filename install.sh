#!/bin/sh
# Install the latest sff binary from GitHub Releases (Linux/macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/ko4edikov/sff/master/install.sh | sh
#
# Overrides via env:
#   SFF_VERSION=v0.2.0          pin a version (default: latest release)
#   SFF_INSTALL_DIR=~/bin       where to install (default: /usr/local/bin if
#                               writable, else ~/.local/bin)
set -eu

REPO="ko4edikov/sff"
BIN="sff"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "sff: unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux | darwin) ;;
  *) echo "sff: unsupported OS: $os — download the Windows zip from Releases" >&2; exit 1 ;;
esac

version="${SFF_VERSION:-}"
if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d '"' -f4)
fi
if [ -z "$version" ]; then
  echo "sff: could not determine the latest release" >&2
  exit 1
fi

# GoReleaser strips the leading v from archive names (v0.1.0 -> 0.1.0).
vnum=${version#v}
url="https://github.com/$REPO/releases/download/$version/${BIN}_${vnum}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "sff: downloading $url"
curl -fsSL "$url" | tar -xz -C "$tmp"

dir="${SFF_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
cp "$tmp/$BIN" "$dir/$BIN"
chmod 0755 "$dir/$BIN"
echo "sff: installed $version to $dir/$BIN"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "sff: add $dir to your PATH to run 'sff'" ;;
esac
