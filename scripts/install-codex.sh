#!/usr/bin/env sh
# Install hostrunner for Codex (CLI, macOS app and IDE extension share the
# same ~/.codex/config.toml and ~/.agents/skills).
#
#   scripts/install-codex.sh                        # download release binary
#   scripts/install-codex.sh --local ./hostrunner   # use a locally built binary
#   options: --version X.Y.Z   --server-name NAME (default hostrunner)
#
# What it does (idempotent):
#   1. fetches the binary (downloaded + checksum-verified, or --local)
#   2. writes a [mcp_servers.<name>] block into ~/.codex/config.toml
#      (replacing a previous managed block, leaving the rest untouched;
#      refuses if an unmanaged [mcp_servers.<name>] table already exists)
#      and links the binary at ~/.local/bin/hostrunner
#   3. copies skills/peer-review and skills/peer-chat into ~/.agents/skills/
#   4. creates ~/.config/hostrunner/config.yaml from the example if missing
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib.sh"

LOCAL=""; VER=""; NAME="hostrunner"
while [ $# -gt 0 ]; do
  case "$1" in
    --local) LOCAL="$2"; shift 2 ;;
    --version) VER="$2"; shift 2 ;;
    --server-name) NAME="$2"; shift 2 ;;
    -h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done

if [ -z "$VER" ]; then
  VER="$(grep '"version"' "$ROOT/.claude-plugin/plugin.json" | head -1 | sed -E 's/.*"version"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')"
fi

# 1. binary
if [ -n "$LOCAL" ]; then
  BIN="$(cd "$(dirname "$LOCAL")" && pwd)/$(basename "$LOCAL")"
  [ -x "$BIN" ] || { echo "not executable: $BIN" >&2; exit 1; }
else
  BIN="$(hostrunner_ensure_binary "$VER")"
fi

# 2. ~/.codex/config.toml
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
CFG="$CODEX_HOME/config.toml"
mkdir -p "$CODEX_HOME"
[ -f "$CFG" ] || : > "$CFG"
BEGIN="# >>> hostrunner (managed by install-codex.sh) >>>"
END="# <<< hostrunner <<<"
tmp="$(mktemp)"
# Drop a previous managed block, then trailing blank lines, so re-running
# replaces the block instead of appending a second one.
awk -v b="$BEGIN" -v e="$END" '
  $0 == b { skip = 1; next }
  $0 == e { skip = 0; next }
  !skip { lines[++n] = $0 }
  END {
    while (n > 0 && lines[n] ~ /^[[:space:]]*$/) n--
    for (i = 1; i <= n; i++) print lines[i]
  }
' "$CFG" > "$tmp"
# A hand-written definition of mcp_servers.<name> outside our markers would
# become a duplicate TOML table and invalidate the whole config. Without a TOML
# parser we match the spellings TOML allows and refuse on any of them:
#   [mcp_servers.NAME]  [mcp_servers."NAME"]  [ mcp_servers . 'NAME' . env ]  # comment
#   mcp_servers.NAME.command = "..."   (dotted key at top level)
#   [mcp_servers] followed by a NAME = { ... } key (inline table)
Q="[\"']?"
HDR="^[[:space:]]*\[[[:space:]]*mcp_servers[[:space:]]*\.[[:space:]]*$Q$NAME$Q[[:space:]]*(\.[^]]*)?\][[:space:]]*(#.*)?$"
DOTTED="^[[:space:]]*mcp_servers[[:space:]]*\.[[:space:]]*$Q$NAME$Q[[:space:]]*[.=]"
conflict=0
if grep -Eq "$HDR" "$tmp" || grep -Eq "$DOTTED" "$tmp"; then
  conflict=1
elif grep -Eq "^[[:space:]]*\[[[:space:]]*mcp_servers[[:space:]]*\][[:space:]]*(#.*)?$" "$tmp" \
  && grep -Eq "^[[:space:]]*$Q$NAME$Q[[:space:]]*=" "$tmp"; then
  conflict=1
fi
if [ "$conflict" = 1 ]; then
  rm -f "$tmp"
  echo "error: $CFG already defines mcp_servers.$NAME outside the block managed by this installer." >&2
  echo "       Remove that definition (or re-run with --server-name <other>) and try again. Nothing was changed." >&2
  exit 1
fi
mv "$tmp" "$CFG"
cat >> "$CFG" <<EOF

$BEGIN
[mcp_servers.$NAME]
command = "$BIN"
args = []
# review/chat jobs are polled via job_status (each call blocks <= 50s)
tool_timeout_sec = 120
startup_timeout_sec = 20
$END
EOF
echo "codex config: [mcp_servers.$NAME] written to $CFG"

mkdir -p "$HOME/.local/bin"
ln -sf "$BIN" "$HOME/.local/bin/hostrunner"
echo "binary: $HOME/.local/bin/hostrunner -> $BIN"

# 3. skills
SKILLS="$HOME/.agents/skills"
mkdir -p "$SKILLS"
for s in peer-review peer-chat; do
  rm -rf "$SKILLS/$s"
  cp -R "$ROOT/skills/$s" "$SKILLS/$s"
  echo "skill: $SKILLS/$s"
done

# 4. hostrunner config
HR_CFG="$HOME/.config/hostrunner/config.yaml"
if [ ! -f "$HR_CFG" ]; then
  mkdir -p "$(dirname "$HR_CFG")"
  cp "$ROOT/examples/config.example.yaml" "$HR_CFG"
  echo "created $HR_CFG — edit allowed_roots before use"
else
  echo "config: $HR_CFG (unchanged)"
fi

cat <<EOF

Done. Next steps:
  1. Edit $HR_CFG: set allowed_roots, and if codex/claude are not on the
     GUI app's PATH set agents.codex.command / agents.claude.command to
     absolute paths (e.g. \$(which claude)).
  2. Restart the Codex app (or run 'codex mcp list' in a terminal) and check
     that '$NAME' is listed.
  3. In Codex, say "peer review my changes" or "ask claude what it thinks
     about this design".
EOF
