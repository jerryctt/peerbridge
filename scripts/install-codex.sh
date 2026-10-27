#!/usr/bin/env sh
# Install peerbridge for Codex (CLI, macOS app and IDE extension share the
# same ~/.codex/config.toml and ~/.agents/skills).
#
#   scripts/install-codex.sh                        # download release binary
#   scripts/install-codex.sh --local ./peerbridge   # use a locally built binary
#   options: --version X.Y.Z   --server-name NAME (default peerbridge)
#
# What it does (idempotent):
#   1. fetches the binary (downloaded + checksum-verified, or --local)
#   2. writes a [mcp_servers.<name>] block into ~/.codex/config.toml
#      (replacing a previous managed block, leaving the rest untouched;
#      refuses if an unmanaged [mcp_servers.<name>] table already exists)
#      and links the binary at ~/.local/bin/peerbridge
#   3. copies skills/peer-review and skills/peer-chat into ~/.agents/skills/
#   4. creates ~/.config/peerbridge/config.yaml from the example if missing
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib.sh"

LOCAL=""; VER=""; NAME="peerbridge"
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
  BIN="$(peerbridge_ensure_binary "$VER")"
fi

# 2. ~/.codex/config.toml
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
CFG="$CODEX_HOME/config.toml"
mkdir -p "$CODEX_HOME"
[ -f "$CFG" ] || : > "$CFG"
BEGIN="# >>> peerbridge (managed by install-codex.sh) >>>"
END="# <<< peerbridge <<<"
# Blocks written before the project was renamed from hostrunner-mcp.
OLD_BEGIN="# >>> hostrunner (managed by install-codex.sh) >>>"
OLD_END="# <<< hostrunner <<<"
tmp="$(mktemp)"
# Drop a previous managed block (new or legacy markers), then trailing blank
# lines, so re-running replaces the block instead of appending a second one.
awk -v b="$BEGIN" -v e="$END" -v ob="$OLD_BEGIN" -v oe="$OLD_END" '
  $0 == b || $0 == ob { skip = 1; next }
  $0 == e || $0 == oe { skip = 0; next }
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
PARENT="$Q"'mcp_servers'"$Q"
HDR="^[[:space:]]*\[[[:space:]]*$PARENT[[:space:]]*\.[[:space:]]*$Q$NAME$Q[[:space:]]*(\.[^]]*)?\][[:space:]]*(#.*)?$"
DOTTED="^[[:space:]]*$PARENT[[:space:]]*\.[[:space:]]*$Q$NAME$Q[[:space:]]*[.=]"
conflict=0
# `mcp_servers = { ... }` is an inline table; TOML forbids adding to it later,
# so appending [mcp_servers.NAME] would invalidate the whole file.
if grep -Eq "^[[:space:]]*$PARENT[[:space:]]*=" "$tmp"; then
  rm -f "$tmp"
  echo "error: $CFG defines mcp_servers as an inline table (mcp_servers = { ... }); a new sub-table cannot be appended to it." >&2
  echo "       Convert it to [mcp_servers.<name>] tables (or add peerbridge inside the inline table by hand) and re-run. Nothing was changed." >&2
  exit 1
fi
if grep -Eq "$HDR" "$tmp" || grep -Eq "$DOTTED" "$tmp"; then
  conflict=1
elif grep -Eq "^[[:space:]]*\[[[:space:]]*$PARENT[[:space:]]*\][[:space:]]*(#.*)?$" "$tmp" \
  && grep -Eq "^[[:space:]]*$Q$NAME$Q[[:space:]]*=" "$tmp"; then
  conflict=1
fi
if [ "$conflict" = 1 ]; then
  rm -f "$tmp"
  echo "error: $CFG already defines mcp_servers.$NAME outside the block managed by this installer." >&2
  echo "       Remove that definition (or re-run with --server-name <other>) and try again. Nothing was changed." >&2
  exit 1
fi
BACKUP="$CFG.bak.peerbridge"
cp "$CFG" "$BACKUP"
mv "$tmp" "$CFG"
cat >> "$CFG" <<EOF

$BEGIN
[mcp_servers.$NAME]
command = "$BIN"
args = []
# review/chat jobs are polled via job_status; keep peerbridge's status_poll
# (config.yaml) below this tool timeout.
tool_timeout_sec = 120
startup_timeout_sec = 20
# Approval prompts: "writes" asks only for tools NOT marked read-only. The
# peer tools (review_start, chat_start, job_status) are read-only, so only
# run_command prompts. Use "approve" to never ask, "prompt" to always ask.
default_tools_approval_mode = "writes"
$END
EOF
# Validate with codex itself when available; restore the original on failure.
if command -v codex >/dev/null 2>&1; then
  if ! codex mcp list >/dev/null 2>"$BACKUP.err"; then
    cp "$BACKUP" "$CFG"
    echo "error: codex rejected the updated $CFG; original restored. codex said:" >&2
    sed 's/^/    /' "$BACKUP.err" >&2
    rm -f "$BACKUP" "$BACKUP.err"
    exit 1
  fi
  rm -f "$BACKUP.err"
fi
rm -f "$BACKUP"
echo "codex config: [mcp_servers.$NAME] written to $CFG"

LINK="$HOME/.local/bin/peerbridge"
mkdir -p "$HOME/.local/bin"
# Never link a file onto itself: --local may name the link, the file it points
# to, or another symlink to that file. `-ef` compares the final inodes after
# following every symlink, so all of those are caught.
if [ -e "$LINK" ] && [ "$LINK" -ef "$BIN" ]; then
  echo "binary: $LINK (already in place)"
elif [ -L "$LINK" ] && [ "$(readlink "$LINK")" = "$BIN" ]; then
  echo "binary: $LINK -> $BIN (unchanged)"
else
  ln -sf "$BIN" "$LINK"
  echo "binary: $LINK -> $BIN"
fi

# 3. skills
SKILLS="$HOME/.agents/skills"
mkdir -p "$SKILLS"
for s in peer-review peer-chat; do
  rm -rf "$SKILLS/$s"
  cp -R "$ROOT/skills/$s" "$SKILLS/$s"
  echo "skill: $SKILLS/$s"
done

# 4. peerbridge config
HR_CFG="$HOME/.config/peerbridge/config.yaml"
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
