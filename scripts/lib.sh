# hostrunner shared shell helpers (POSIX sh). Source this file; do not execute.
# Provides: hostrunner_ensure_binary VERSION  -> prints path to a verified binary.

REPO="${REPO:-jerryctt/hostrunner-mcp}"

hostrunner_platform() {
  os="$(uname -s)"; arch="$(uname -m)"
  case "$os" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *) echo "hostrunner: unsupported OS '$os' — install manually (see README)" >&2; return 1 ;;
  esac
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "hostrunner: unsupported arch '$arch'" >&2; return 1 ;;
  esac
  echo "$os $arch"
}

# hostrunner_ensure_binary VERSION
# Downloads hostrunner_<ver>_<os>_<arch>.tar.gz from GitHub Releases on first
# use, verifies it against the release checksums.txt, caches it under
# ${XDG_CACHE_HOME:-$HOME/.cache}/hostrunner and prints the binary path.
hostrunner_ensure_binary() {
  ver="$1"
  platform="$(hostrunner_platform)" || return 1
  os="${platform% *}"; arch="${platform#* }"
  cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/hostrunner"
  bin="$cache_dir/hostrunner-$ver-$os-$arch"
  if [ -x "$bin" ]; then
    echo "$bin"; return 0
  fi
  mkdir -p "$cache_dir"
  archive="hostrunner_${ver}_${os}_${arch}.tar.gz"
  base="https://github.com/$REPO/releases/download/v$ver"
  tmp="$(mktemp -d)"
  echo "hostrunner: downloading $archive (v$ver)..." >&2
  if ! curl -fsSL -o "$tmp/$archive" "$base/$archive" || ! curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"; then
    rm -rf "$tmp"; echo "hostrunner: download failed" >&2; return 1
  fi
  want="$(grep " ${archive}\$" "$tmp/checksums.txt" | awk '{print $1}')"
  if [ -z "${want:-}" ]; then
    rm -rf "$tmp"; echo "hostrunner: $archive not listed in checksums.txt" >&2; return 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    got="$(sha256sum "$tmp/$archive" | awk '{print $1}')"
  else
    got="$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')"
  fi
  if [ "$want" != "$got" ]; then
    rm -rf "$tmp"
    echo "hostrunner: checksum mismatch for $archive (expected $want, got $got)" >&2; return 1
  fi
  tar -xzf "$tmp/$archive" -C "$tmp"
  if [ ! -f "$tmp/hostrunner" ]; then
    rm -rf "$tmp"; echo "hostrunner: 'hostrunner' not found in archive" >&2; return 1
  fi
  chmod +x "$tmp/hostrunner"
  mv "$tmp/hostrunner" "$bin"
  rm -rf "$tmp"
  echo "hostrunner: installed $bin" >&2
  echo "$bin"
}
