#!/usr/bin/env sh
# hostrunner plugin launcher (macOS/Linux) for Claude Desktop / Claude Code.
# Downloads the prebuilt binary matching this host on first run (verified
# against the release checksums.txt), caches it, then execs it. Windows is not
# supported by this launcher — install the binary manually (see README).
set -eu

PLUGIN_ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
PLUGIN_JSON="$PLUGIN_ROOT/.claude-plugin/plugin.json"
. "$PLUGIN_ROOT/scripts/lib.sh"

VER="$(grep '"version"' "$PLUGIN_JSON" | head -1 | sed -E 's/.*"version"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')"
if [ -z "${VER:-}" ]; then
  echo "hostrunner launcher: cannot read version from $PLUGIN_JSON" >&2
  exit 1
fi

BIN="$(hostrunner_ensure_binary "$VER")"
exec "$BIN" "$@"
