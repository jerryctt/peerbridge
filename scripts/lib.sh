# peerbridge shared shell helpers (POSIX sh). Source this file; do not execute.
# Provides: peerbridge_ensure_binary VERSION  -> prints path to a verified binary.

REPO="${REPO:-jerryctt/peerbridge}"

peerbridge_platform() {
  os="$(uname -s)"; arch="$(uname -m)"
  case "$os" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *) echo "peerbridge: unsupported OS '$os' — install manually (see README)" >&2; return 1 ;;
  esac
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    i386|i486|i586|i686|x86) arch=386 ;;
    armv7*|armv8l) arch=armv7 ;;   # 32-bit userland on ARMv7/ARMv8 (Raspberry Pi OS 32-bit)
    armv6*) arch=armv6 ;;
    *) echo "peerbridge: unsupported arch '$arch'" >&2; return 1 ;;
  esac
  if [ "$os" = darwin ] && [ "$arch" != amd64 ] && [ "$arch" != arm64 ]; then
    echo "peerbridge: no macOS build for '$arch'" >&2; return 1
  fi
  echo "$os $arch"
}

# peerbridge_ensure_binary VERSION
# Downloads peerbridge_<ver>_<os>_<arch>.tar.gz from GitHub Releases on first
# use, verifies it against the release checksums.txt, caches it under
# ${XDG_CACHE_HOME:-$HOME/.cache}/peerbridge and prints the binary path.
peerbridge_ensure_binary() {
  ver="$1"
  platform="$(peerbridge_platform)" || return 1
  os="${platform% *}"; arch="${platform#* }"
  cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/peerbridge"
  bin="$cache_dir/peerbridge-$ver-$os-$arch"
  if [ -x "$bin" ]; then
    echo "$bin"; return 0
  fi
  mkdir -p "$cache_dir"
  archive="peerbridge_${ver}_${os}_${arch}.tar.gz"
  base="https://github.com/$REPO/releases/download/v$ver"
  tmp="$(mktemp -d)"
  echo "peerbridge: downloading $archive (v$ver)..." >&2
  if ! curl -fsSL -o "$tmp/$archive" "$base/$archive" || ! curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"; then
    rm -rf "$tmp"; echo "peerbridge: download failed" >&2; return 1
  fi
  want="$(grep " ${archive}\$" "$tmp/checksums.txt" | awk '{print $1}')"
  if [ -z "${want:-}" ]; then
    rm -rf "$tmp"; echo "peerbridge: $archive not listed in checksums.txt" >&2; return 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    got="$(sha256sum "$tmp/$archive" | awk '{print $1}')"
  else
    got="$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')"
  fi
  if [ "$want" != "$got" ]; then
    rm -rf "$tmp"
    echo "peerbridge: checksum mismatch for $archive (expected $want, got $got)" >&2; return 1
  fi
  tar -xzf "$tmp/$archive" -C "$tmp"
  if [ ! -f "$tmp/peerbridge" ]; then
    rm -rf "$tmp"; echo "peerbridge: 'peerbridge' not found in archive" >&2; return 1
  fi
  chmod +x "$tmp/peerbridge"
  mv "$tmp/peerbridge" "$bin"
  rm -rf "$tmp"
  echo "peerbridge: installed $bin" >&2
  echo "$bin"
}
