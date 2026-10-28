[English](README.md) | 繁體中文

# peerbridge

[![CI](https://github.com/jerryctt/peerbridge/actions/workflows/ci.yml/badge.svg)](https://github.com/jerryctt/peerbridge/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

給 **Claude** 與 **Codex** 共用的本機 MCP server + plugin：在任一邊的 app 裡，都能請*另一家*的 agent 對你的程式碼做唯讀 code review，或進行多輪討論——全部在你自己的機器上、用你自己的 CLI 登入執行。

> **v0.5.0：移除 `run_command`。** peerbridge 不再執行任意允許清單中的指令，只會以 server 固定的唯讀參數啟動 peer CLI，所有工具現在都是唯讀。`config.yaml` 裡的 `allowed_commands` 會被忽略（並記一行警告），可以刪掉。

> **自 `hostrunner-mcp` 改名（v0.3.2）。** 舊的 GitHub 網址會自動轉址。若你裝過舊版：binary、MCP server、plugin 與設定目錄現在都叫 `peerbridge`。既有的 `~/.config/hostrunner/config.yaml` 與 `HOSTRUNNER_CONFIG` 在你搬移前仍會被讀取；重新執行 `scripts/install-codex.sh` / `scripts/install.ps1` 會把舊的 `[mcp_servers.hostrunner]` 區塊換成 `[mcp_servers.peerbridge]`；Claude Desktop 請重新安裝 **peerbridge** plugin（並移除舊的 **hostrunner**），手動註冊 binary 的話請更新 `claude_desktop_config.json`。權限規則也隨之改成 `mcp__plugin_peerbridge_peerbridge__*`。MCP 工具名稱（`review_start`、`chat_start`、`job_status`、`run_command`）不變。

---

## 目的 / 為什麼

Coding agent 各自活在自己的 app 裡。[Claude](https://claude.ai)（Desktop / Cowork / Code）在沙箱裡改程式，沙箱跑不了主機上的 `codex` CLI；Codex app 也沒辦法問 Claude 任何事。但「讓另一個模型看一眼」是抓 bug 與盲點最便宜的方法之一。

**peerbridge** 雙向補上這個缺口：

- 以**原生程序**在你的 Mac 或 Linux 上執行，由 Claude Desktop/Code **或** Codex（CLI、macOS app、IDE extension）透過 stdio 啟動。
- 看得到主機檔案系統、`codex` 與 `claude` 二進位檔，以及它們既有的登入狀態。
- GUI 裡的 agent 負責改檔；peerbridge 讓它呼叫 **peer** agent（Claude 裡叫 `codex`、Codex 裡叫 `claude`）來 **review** 變更，或就一個設計**討論**好幾輪——永遠唯讀。
- 得到緊湊的 **編輯 → peer review → 編輯** 循環，或兩個模型之間的**辯論**，不用離開編輯器。

---

## 運作方式

```
┌────────────────────────────┐        ┌────────────────────────────┐
│  Claude Desktop / Code     │        │  Codex app / CLI / IDE     │
│  （agent 改檔）             │        │  （agent 改檔）             │
│   review_start / chat_start│        │   review_start / chat_start│
└──────────────┬─────────────┘        └──────────────┬─────────────┘
               │ stdio (MCP)                         │ stdio (MCP)
               ▼                                     ▼
┌───────────────────────────────────────────────────────────────────┐
│  主機 — peerbridge（原生程序，單一 binary）                          │
│                                                                   │
│  peer = 另一家的 agent（依 MCP client 名稱自動推斷）                 │
│                                                                   │
│   agent=codex  → codex review …  /  codex exec --sandbox read-only│
│   agent=claude → claude -p --strict-mcp-config --permission-mode dontAsk │
│                         ▼                                         │
│                 git repo  /Users/…/proj   （peer 只能讀）           │
└───────────────────────────────────────────────────────────────────┘
```

---

## 功能

- **`review_start` / `job_status`** — 由 peer agent 對 uncommitted 變更、base branch diff 或某個 commit 做唯讀 review：`codex` 跑 `codex review`；`claude` 用 review prompt 跑 `claude -p`。Review 以**背景 job** 執行，因為它常超過 MCP 單次呼叫的時限（Claude Desktop 約 180 秒、Codex 的 `tool_timeout_sec`）；start/status 都很快回，review 本身可以跑到你設定的 `timeout`。
- **`chat_start` / `job_status`** — 送一則訊息給可以讀 repo 的 peer；結果帶 `session_id`，可續接同一段對話（`claude -p --resume`、`codex exec resume`）。回合由 GUI 裡的 agent 驅動。
- **自動選 peer** — 不帶 `agent` 時，Claude client 會找 codex、Codex client 會找 claude；要指定就傳 `agent`。
- **唯讀 peer** — claude peer 只拿到讀檔工具（`--tools Read,Grep,Glob`，沒有 shell；review 用的 diff 由 peerbridge 以唯讀 git 算好、經 stdin 餵給 peer），且不載入任何使用者設定與 MCP server（`--setting-sources "" --strict-mcp-config`）；codex peer 跑在 codex 自己的 `--sandbox read-only` 並加 `--ignore-user-config`。兩者都無法遞迴呼叫 peerbridge，你原本的 CLI 登入照常有效。
- **`codex_review_start` / `codex_review_status`** — 已棄用的別名，等同 `review_start(agent=codex)` / `job_status`；既有安裝照常可用。
- **嚴格安全** — 根目錄限制、符號連結解析後的路徑檢查、peer 的 argv 固定且唯讀、不提供任意指令執行、絕不使用 shell。
- **可設定** — 允許的根目錄、timeout、輸出上限、各 agent 的指令路徑與額外旗標。
- **審計日誌** — 每次工具呼叫都透過 zerolog 寫到 stderr。

---

## 需求

| 需求 | 說明 |
|---|---|
| **codex CLI** | `agent=codex` 需要——主機上已安裝並登入 |
| **claude CLI** | `agent=claude` 需要——主機上已安裝並登入（隨 Claude Code 提供） |
| **Claude Desktop / Claude Code** *或* **Codex app / CLI** | 任一方負責註冊並啟動 MCP server（兩邊都可以） |
| **Go 1.25+** | 僅從原始碼建置時需要；預建 release 不需要 Go |

---

## 安裝

### 方式 1：`go install`（有 Go 的話建議）

```bash
go install github.com/jerryctt/peerbridge/cmd/peerbridge@latest
```

二進位檔會放在 `$GOPATH/bin/peerbridge`（通常是 `~/go/bin/peerbridge`）。

### 方式 2：下載 release 二進位檔

從 [Releases 頁面](https://github.com/jerryctt/peerbridge/releases)下載對應平台的預建檔，解壓後把 `peerbridge` 放進 `$PATH`（例如 `/usr/local/bin/peerbridge`；Windows 則把 `peerbridge.exe` 放在任何地方）。

每個 release 都附這些壓縮檔（靜態 binary，不需 runtime）：

| OS | 架構 | 檔名 |
|---|---|---|
| macOS | `amd64`（Intel）、`arm64`（Apple silicon） | `peerbridge_<ver>_darwin_<arch>.tar.gz` |
| Linux | `amd64`、`arm64`、`386`（32-bit x86）、`armv6`、`armv7`（32-bit ARM，如 Raspberry Pi） | `peerbridge_<ver>_linux_<arch>.tar.gz` |
| Windows | `amd64`、`arm64`、`386` | `peerbridge_<ver>_windows_<arch>.zip` |

下面的安裝腳本會自動選對檔案（`uname -m` / `PROCESSOR_ARCHITECTURE`）並用 release 的 `checksums.txt` 驗證。

### 方式 3：從原始碼建置

```bash
git clone https://github.com/jerryctt/peerbridge.git
cd peerbridge
make build          # 產生 ./peerbridge
```

### 方式 4：以 Claude plugin 安裝（marketplace）

**Claude Desktop：** Add marketplace → 輸入 `jerryctt/peerbridge`，安裝 **peerbridge** plugin。

**Claude Code：**
```
/plugin marketplace add jerryctt/peerbridge
```
然後從清單安裝 `peerbridge` plugin。

透過 marketplace 安裝會一次裝好 MCP server（由內附 launcher 自動啟動）以及 `peer-review`、`peer-chat` 兩個 skill。

**前置：建立設定檔。** server 預設讀 `~/.config/peerbridge/config.yaml`。啟動 Claude Desktop/Code 前先從 `examples/config.example.yaml` 建立：

```bash
mkdir -p ~/.config/peerbridge
cp examples/config.example.yaml ~/.config/peerbridge/config.yaml
# 依你的路徑修改
```

可用 `-config` 旗標或 `PEERBRIDGE_CONFIG` 環境變數覆寫設定檔路徑。

> **macOS GUI app 與環境變數：** macOS 不會把 shell 環境變數傳給 Claude Desktop 或 Codex app 這類 GUI，所以在 shell profile 設 `PEERBRIDGE_CONFIG`（或 `PATH`）對 GUI 啟動的 server 無效。請把設定檔放在預設路徑；若找不到 `codex`/`claude`，把 `agents.<name>.command` 設成絕對路徑。

**macOS/Linux：** 內附的 `scripts/launch.sh` 第一次執行時會下載對應平台的 release 二進位檔，並以 release 的 `checksums.txt` 驗證後才執行。不需手動安裝。

**Windows：** marketplace plugin 的 launcher 是 POSIX shell 腳本，所以在 Windows 上 plugin 的 MCP server 不會自己啟動（skill 仍可用）。請改用 `scripts\install.ps1`——見下方「在 Windows 安裝」——它會直接把 `peerbridge.exe` 註冊進 `claude_desktop_config.json`。

> **信任提醒：** launcher 會從 GitHub Releases 下載並執行二進位檔（經 checksum 驗證）。有疑慮請檢視 `scripts/launch.sh` 與 `scripts/lib.sh`。

### 在 Windows 安裝（Codex 及／或 Claude Desktop）

```powershell
git clone https://github.com/jerryctt/peerbridge.git
cd peerbridge
powershell -ExecutionPolicy Bypass -File scripts\install.ps1          # Codex + Claude Desktop
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Codex   # 或 -Claude 只裝其中一個
# ... -Local .\peerbridge.exe 使用本機建置的 binary；-NoClaudeConfigWrite 只印出 JSON 片段不寫入
```

支援 Windows PowerShell 5.1 與 PowerShell 7。它會下載並驗證對應的 zip、把 `peerbridge.exe` 裝到 `%LOCALAPPDATA%\peerbridge\`、從範例建立 `%APPDATA%\peerbridge\config.yaml`、把 Codex 的 `[mcp_servers.peerbridge]` 區塊寫進 `%USERPROFILE%\.codex\config.toml`、skill 裝到 `%USERPROFILE%\.agents\skills\`，並把 `mcpServers.peerbridge` 合併進 `%APPDATA%\Claude\claude_desktop_config.json`（會留一份 `.bak.peerbridge`）。改好 `allowed_roots`（Windows 路徑如 `C:/Users/you/code` 皆可）後重啟 app。

Windows 注意事項：`codex` 與 `claude` 通常是 npm 的 `.cmd` shim——GUI app 找不到時，把 `agents.codex.command` / `agents.claude.command` 設成 `(Get-Command claude).Source` 印出的完整路徑。timeout 時 peerbridge 會用 `taskkill /T` 殺掉 peer 的整棵程序樹。

### 安裝到 Codex（CLI、macOS app、IDE extension）

```bash
git clone https://github.com/jerryctt/peerbridge.git
cd peerbridge
scripts/install-codex.sh            # 或：scripts/install-codex.sh --local ./peerbridge
```

這個腳本會下載並驗證 release 二進位檔（連結到 `~/.local/bin/peerbridge`）、在 `~/.codex/config.toml` 寫入 `[mcp_servers.peerbridge]` 區塊（含 `tool_timeout_sec = 120`）、把 `peer-review` 與 `peer-chat` skill 裝進 `~/.agents/skills/`，並在不存在時建立 `~/.config/peerbridge/config.yaml`。改好 `allowed_roots` 後重啟 Codex app（CLI、macOS app、IDE extension 共用這份設定）。在 Codex 裡說「peer review my changes」或「ask claude what it thinks about this design」即可。

手動等效設定：

```toml
# ~/.codex/config.toml
[mcp_servers.peerbridge]
command = "/Users/you/.local/bin/peerbridge"
tool_timeout_sec = 120
startup_timeout_sec = 20
```

---

## 設定

server 依下列順序決定設定檔：
1. `-config` 旗標指定的路徑。
2. `PEERBRIDGE_CONFIG` 環境變數。
3. 下列中第一個存在的檔案，都不存在則取第一個：`~/.config/peerbridge/config.yaml`（所有平台），其次是 OS 的使用者設定目錄——Windows 的 `%APPDATA%\peerbridge\config.yaml`、macOS 的 `~/Library/Application Support/peerbridge/config.yaml`。

**建議**放在 `~/.config/peerbridge/config.yaml`。GUI app 不繼承 shell 環境變數，shell profile 裡的 `PEERBRIDGE_CONFIG` 對 Claude Desktop 或 Codex app 啟動的 server 看不到。

從範例建立設定檔——**裡面的路徑是佔位符，請改成你自己的**：

```bash
mkdir -p ~/.config/peerbridge
cp examples/config.example.yaml ~/.config/peerbridge/config.yaml
```

```yaml
# 工具允許存取的主機絕對路徑；解析後落在這些根目錄之外的請求一律拒絕。
allowed_roots:
  - /Users/yourname/code

# 主機上單一 job（一次 review 或一輪 chat）的最長時間。
# 必須帶單位（600s、10m）。job 在背景執行，所以可以遠大於 MCP client 的單次呼叫時限。
timeout: 600s

# 單次工具呼叫回傳的最大位元組數（超過會截斷並附註）。
max_output_bytes: 200000

# job_status 回「running」前最多等多久。越長輪詢越少、權限詢問也越少。
# 必須低於 client 的單次呼叫時限（Codex tool_timeout_sec：安裝腳本寫 120；Claude Desktop 約 180 秒）。
status_poll: 100s

# 是否即時把 peer CLI 輸出 tee 到 server 的 stderr（預設 true）。
# Claude Desktop 會收進 ~/Library/Logs/Claude/mcp-server-peerbridge.log。
stream_output: true

# Peer agent。兩者皆可省略，預設如下。GUI app 找不到 CLI 時請填絕對路徑
#（例如 `which claude` 的輸出）。
agents:
  codex:
    command: codex
    extra_args: []              # 例如 ["-c", "model=o3"]
  claude:
    command: claude
    extra_args: []              # 例如 ["--model", "opus"]

# 舊欄位，仍然有效，會附加到 agents.codex.extra_args：
# codex_extra_args: ["-c", "model=o3"]
```

> **注意：** 設定檔**只在 server 啟動時讀一次**。改完 `config.yaml` 後要完全重啟 Claude Desktop 或 Codex app（它們會重新啟動 server）才會生效。啟動時會記錄實際使用的值（找 `config loaded` 那行——顯示設定檔路徑、timeout 與各 agent 的指令）。

---

## 註冊到 Claude Desktop

在 Claude Desktop 設定檔（macOS 通常是 `~/Library/Application Support/Claude/claude_desktop_config.json`）加入：

```json
{
  "mcpServers": {
    "peerbridge": {
      "command": "/usr/local/bin/peerbridge",
      "args": ["-config", "/Users/yourname/.config/peerbridge/config.yaml"]
    }
  }
}
```

把 `/usr/local/bin/peerbridge` 改成實際的二進位檔路徑（例如 `~/go/bin/peerbridge`），設定檔路徑也一併調整。`examples/claude_desktop_config.example.json` 有相同範例。

改完後重啟 Claude Desktop，MCP servers 清單應出現 **peerbridge**。

---

## 可用工具

| 工具 | 用途 |
|---|---|
| `review_start(folder, agent?, scope?, base?, commit?, prompt?)` | 由 peer agent 開始唯讀 review；回傳 `job_id`。 |
| `chat_start(folder, message, agent?, session_id?)` | 送一則訊息給 peer；回傳 `job_id`。帶上前一次回覆的 `session_id` 即可續接同一段對話。 |
| `job_status(job_id)` | long-poll（≤50 秒）取得 job 結果。 |
| `codex_review_start`、`codex_review_status` | 已棄用，等同 `review_start(agent=codex)` / `job_status`。 |

所有 `folder` 參數都是**主機**絕對路徑（例如 `/Users/you/proj`），絕不是 `/sessions/…` 沙箱路徑。

### `review_start`

| 參數 | 型別 | 必填 | 說明 |
|---|---|---|---|
| `folder` | string | 是 | git repo 的主機絕對路徑 |
| `agent` | string | 否 | `codex` 或 `claude`。省略則自動選另一家（Claude client → codex、Codex client → claude） |
| `scope` | string | 否 | `uncommitted`（預設——staged+unstaged+untracked）、`base`（相對 base branch）、`commit`（特定 commit） |
| `base` | string | 否 | base branch 或 ref；`scope=base` 時必填（例如 `main`） |
| `commit` | string | 否 | commit SHA；`scope=commit` 時必填 |
| `prompt` | string | 否 | 自訂 review 指示（例如 `"focus on error handling"`）。只在明確要聚焦時使用；可與任何 `scope` 搭配。 |

`agent=codex` 執行 `codex review`（原生，自行計算 diff）。`agent=claude` 以 review prompt 執行 `claude -p`，要求它用唯讀 git 指令檢視變更。

**回傳：**

```
Review started in the background.
job_id: 3fa4c19e02d1
agent: codex
mode: uncommitted
folder: /Users/you/proj
```

### `chat_start`

| 參數 | 型別 | 必填 | 說明 |
|---|---|---|---|
| `folder` | string | 是 | peer 可以讀的主機絕對路徑 |
| `message` | string | 是 | 要說的話。請附上 peer 需要的脈絡——它只看得到 repo 與這段對話。 |
| `agent` | string | 否 | `codex` 或 `claude`；省略則自動選 peer |
| `session_id` | string | 否 | 前一次 `job_status` 回傳的 `session_id`，用來續接該對話 |

### `job_status`

最多阻塞約 50 秒等待完成，然後回傳完成結果或 `running` 通知——重複呼叫直到完成。完成的結果保留約 30 分鐘。

| 參數 | 型別 | 必填 | 說明 |
|---|---|---|---|
| `job_id` | string | 是 | `review_start` / `chat_start` 回傳的 id |

**回傳（review 完成）：**

```
status: completed (job 3fa4c19e02d1, agent codex, kind review, elapsed 4m12s)

Mode: uncommitted (codex exit 0)

--- Codex review ---
… 發現的問題 …
```

**回傳（chat 完成）：**

```
status: completed (job 9b1d…, agent claude, kind chat, elapsed 1m05s)

session_id: 6c2f0a4e-…

--- Claude reply ---
… peer 的回答 …
```

**回傳（仍在執行）：**

```
status: running (job 3fa4c19e02d1, agent codex, kind review, elapsed 1m40s)
Still in progress. Call job_status again with the same job_id.
```

從 Codex 端發起的討論範例（peer = claude）：

```
chat_start(folder="/Users/you/proj", message="Critique the retry design in the uncommitted diff.")
job_status(job_id="…")   → session_id: abc…  --- Claude reply --- …
chat_start(folder="/Users/you/proj", session_id="abc…", message="Point 2 only applies to POST; does that change your view?")
```

---

## 權限詢問

每個 host 預設都會在執行 MCP 工具前詢問；一次 review 是一個 `review_start` 加上好幾次 `job_status` 輪詢，所以預設下大約每 50 秒就會被問一次。有三個地方可以調：

- **減少輪詢次數。** `config.yaml` 的 `status_poll` 是 `job_status` 回「running」前最多等多久。範例設 `100s`（配合安裝腳本寫入的 `tool_timeout_sec = 120` 與 Claude Desktop 約 180 秒的上限都安全），內建預設 `50s`。一定要低於 client 的單次呼叫時限。
- **Codex。** 安裝腳本會在 `[mcp_servers.peerbridge]` 區塊寫入 `default_tools_approval_mode = "writes"`：Codex 只會對*沒有*標為唯讀的工具詢問。`review_start`、`chat_start`、`job_status` 都帶 MCP 的 `readOnlyHint` 註解（peer 從不改任何東西），所以不會被問。peerbridge 的工具全都是唯讀，所以 Codex 完全不會問；想每次都確認就改成 `"prompt"`。
- **Claude。** Claude Desktop / Cowork 在每個工具第一次詢問時選 **Always allow**（會以工具為單位記住）。Claude Code 則在 `~/.claude/settings.json` 加權限規則：`{"permissions": {"allow": ["mcp__peerbridge__*"]}}`——若是透過 marketplace plugin 安裝，server 名稱是 `plugin_peerbridge_peerbridge`，要寫 `"mcp__plugin_peerbridge_peerbridge__*"`。Claude Code 目前不會依 `readOnlyHint` 自動放行 MCP 工具，所以要明確加規則。萬用字元是安全的：peerbridge 的工具全都是唯讀。

## Skills

`skills/` 下附兩個 skill，採用 Claude（plugin）與 Codex（`~/.agents/skills/`）共用的 `SKILL.md` 格式：

- **`peer-review`** — 以 `review_start` / `job_status` 跑「編輯 → peer review → 編輯」循環；agent 中立，兩邊都能用，可由 codex 或 claude 來 review。
- **`peer-chat`** — 以 `chat_start` / `job_status` 進行多輪討論，保留 `session_id`。

原本只給 Claude 用的 `codex-loop` skill 已移除，由 `peer-review` 取代（說「review my changes with codex」即可指定由 codex review）。

Claude marketplace plugin 與 `scripts/install-codex.sh` 會自動安裝它們。用類似「peer review my changes」、「ask claude/codex what it thinks about this」、「review my changes with codex」的話即可觸發。

---

## 使用方式

典型的 **編輯 → peer review → 編輯**，兩邊都一樣：

1. **開啟專案**：在 Claude 把資料夾分享給 Cowork session，或在 Codex 開啟專案。
2. **用 agent 自己的工具改檔。**
3. **開始 review。** `review_start(folder="/Users/yourname/code/yourproject")`——主機路徑，不是 `/sessions/…`。server 自動選另一家的 agent 並回傳 `job_id`。
4. **輪詢** `job_status` 直到 `status: completed`。每次最多阻塞約 50 秒。
5. **迭代。** 修掉問題、重新 review，直到乾淨。

要**討論**，用 `chat_start` 送一則聚焦的訊息，從 `job_status` 讀回覆，再帶回傳的 `session_id` 繼續。

---

## 安全性

- **根目錄限制。** 每個路徑參數都解析（含符號連結）後對照 `allowed_roots`。
- **無 shell。** 指令以 argv 陣列經 `os/exec` 執行，沒有 `sh -c`、沒有 glob 展開、沒有 shell injection。
- **唯讀 peer。** peer 的 argv 由 server 固定。codex 用 `codex review` / `codex exec --sandbox read-only --ignore-user-config`（OS 層沙箱）。claude 沒有沙箱，所以完全不給 shell：`-p --strict-mcp-config --setting-sources "" --permission-mode dontAsk --tools Read,Grep,Glob --allowedTools Read,Grep,Glob --disallowedTools Edit,Write,NotebookEdit,Bash`。claude review 的 `git status`/`git diff`/`git show` 由 peerbridge 自己執行並經 stdin 餵給 peer——唯讀且加固：ref 會先驗證（不可能像選項）、`--no-ext-diff --no-textconv`、`core.fsmonitor=false`、`core.hooksPath=/dev/null`、忽略 global/system git config（`GIT_CONFIG_GLOBAL=/dev/null` 等）並設 `GIT_OPTIONAL_LOCKS=0`，所以不會跑任何 diff helper、filter 或 hook，也不會寫入任何東西。只有 `agents.<name>.command` 與 `extra_args` 來自設定。
- **不繼承權限、不遞迴。** `--setting-sources ""` 讓使用者自己的 Claude 權限規則與 hook 不會進到 peer（`--allowedTools` 是疊加在設定之上的，這樣它才是完整清單）；`--strict-mcp-config`（claude）與 `--ignore-user-config`（codex）讓 peer 啟動時沒有使用者的任何 MCP server，所以不會載入 peerbridge 自己。刻意不用 `--bare`：它會關閉 OAuth/keychain 認證，訂閱登入會失效。`-c mcp_servers={}` 也被否決：config 覆寫是合併而非取代。
- **不提供任意指令執行。** 沒有任何工具能跑任意指令：peerbridge 只會啟動帶上述固定 argv 的 peer CLI，以及它自己加固過的唯讀 git。（`run_command` 已在 v0.5.0 移除：即使有允許清單，`claude`/`codex`/`git` 都能靠參數繞過唯讀設計。）
- **原生程序，非 Docker。** 容器化會失去主機 CLI 與登入狀態的存取。
- **僅主機路徑。** 以 `/sessions/` 開頭的路徑一律拒絕並給出提示。

---

## 開發

```bash
make build   # 建置
make test    # 測試
make vet     # vet
```

---

## 授權

MIT — 見 [LICENSE](LICENSE)。
