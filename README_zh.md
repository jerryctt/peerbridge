[English](README.md) | 繁體中文

# hostrunner-mcp

[![CI](https://github.com/jerryctt/hostrunner-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/jerryctt/hostrunner-mcp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

給 **Claude** 與 **Codex** 共用的本機 MCP server + plugin：在任一邊的 app 裡，都能請*另一家*的 agent 對你的程式碼做唯讀 code review，或進行多輪討論——全部在你自己的機器上、用你自己的 CLI 登入執行。

---

## 目的 / 為什麼

Coding agent 各自活在自己的 app 裡。[Claude](https://claude.ai)（Desktop / Cowork / Code）在沙箱裡改程式，沙箱跑不了主機上的 `codex` CLI；Codex app 也沒辦法問 Claude 任何事。但「讓另一個模型看一眼」是抓 bug 與盲點最便宜的方法之一。

**hostrunner-mcp** 雙向補上這個缺口：

- 以**原生程序**在你的 Mac 或 Linux 上執行，由 Claude Desktop/Code **或** Codex（CLI、macOS app、IDE extension）透過 stdio 啟動。
- 看得到主機檔案系統、`codex` 與 `claude` 二進位檔，以及它們既有的登入狀態。
- GUI 裡的 agent 負責改檔；hostrunner 讓它呼叫 **peer** agent（Claude 裡叫 `codex`、Codex 裡叫 `claude`）來 **review** 變更，或就一個設計**討論**好幾輪——永遠唯讀。
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
│  主機 — hostrunner（原生程序，單一 binary）                          │
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
- **唯讀 peer** — claude peer 只拿到讀檔工具（`--tools Read,Grep,Glob`，沒有 shell；review 用的 diff 由 hostrunner 以唯讀 git 算好塞進 prompt），且不載入任何使用者設定與 MCP server（`--setting-sources "" --strict-mcp-config`）；codex peer 跑在 codex 自己的 `--sandbox read-only` 並加 `--ignore-user-config`。兩者都無法遞迴呼叫 hostrunner，你原本的 CLI 登入照常有效。
- **`codex_review_start` / `codex_review_status`** — 已棄用的別名，等同 `review_start(agent=codex)` / `job_status`；既有安裝照常可用。
- **`run_command`** — 在主機資料夾內以 argv 陣列（絕不經過 shell）執行任何允許清單中的 CLI（例如 `git`）。
- **嚴格安全** — 根目錄限制、符號連結解析後的路徑檢查、peer 的 argv 固定且唯讀、`run_command` 的指令允許清單、絕不使用 shell。
- **可設定** — 允許的根目錄、允許的指令、timeout、輸出上限、各 agent 的指令路徑與額外旗標。
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
go install github.com/jerryctt/hostrunner-mcp/cmd/hostrunner@latest
```

二進位檔會放在 `$GOPATH/bin/hostrunner`（通常是 `~/go/bin/hostrunner`）。

### 方式 2：下載 release 二進位檔

從 [Releases 頁面](https://github.com/jerryctt/hostrunner-mcp/releases)下載對應平台的預建檔，解壓後把 `hostrunner` 放進 `$PATH`（例如 `/usr/local/bin/hostrunner`）。

### 方式 3：從原始碼建置

```bash
git clone https://github.com/jerryctt/hostrunner-mcp.git
cd hostrunner-mcp
make build          # 產生 ./hostrunner
```

### 方式 4：以 Claude plugin 安裝（marketplace）

**Claude Desktop：** Add marketplace → 輸入 `jerryctt/hostrunner-mcp`，安裝 **hostrunner** plugin。

**Claude Code：**
```
/plugin marketplace add jerryctt/hostrunner-mcp
```
然後從清單安裝 `hostrunner` plugin。

透過 marketplace 安裝會一次裝好 MCP server（由內附 launcher 自動啟動）以及 `peer-review`、`peer-chat`、`codex-loop` 三個 skill。

**前置：建立設定檔。** server 預設讀 `~/.config/hostrunner/config.yaml`。啟動 Claude Desktop/Code 前先從 `examples/config.example.yaml` 建立：

```bash
mkdir -p ~/.config/hostrunner
cp examples/config.example.yaml ~/.config/hostrunner/config.yaml
# 依你的路徑修改
```

可用 `-config` 旗標或 `HOSTRUNNER_CONFIG` 環境變數覆寫設定檔路徑。

> **macOS GUI app 與環境變數：** macOS 不會把 shell 環境變數傳給 Claude Desktop 或 Codex app 這類 GUI，所以在 shell profile 設 `HOSTRUNNER_CONFIG`（或 `PATH`）對 GUI 啟動的 server 無效。請把設定檔放在預設路徑；若找不到 `codex`/`claude`，把 `agents.<name>.command` 設成絕對路徑。

**macOS/Linux：** 內附的 `scripts/launch.sh` 第一次執行時會下載對應平台的 release 二進位檔，並以 release 的 `checksums.txt` 驗證後才執行。不需手動安裝。

**Windows：** launcher 不支援 Windows。請手動安裝（方式 2）並依「註冊到 Claude Desktop」一節設定 `claude_desktop_config.json`。

> **信任提醒：** launcher 會從 GitHub Releases 下載並執行二進位檔（經 checksum 驗證）。有疑慮請檢視 `scripts/launch.sh` 與 `scripts/lib.sh`。

### 安裝到 Codex（CLI、macOS app、IDE extension）

```bash
git clone https://github.com/jerryctt/hostrunner-mcp.git
cd hostrunner-mcp
scripts/install-codex.sh            # 或：scripts/install-codex.sh --local ./hostrunner
```

這個腳本會下載並驗證 release 二進位檔（連結到 `~/.local/bin/hostrunner`）、在 `~/.codex/config.toml` 寫入 `[mcp_servers.hostrunner]` 區塊（含 `tool_timeout_sec = 120`）、把 `peer-review` 與 `peer-chat` skill 裝進 `~/.agents/skills/`，並在不存在時建立 `~/.config/hostrunner/config.yaml`。改好 `allowed_roots` 後重啟 Codex app（CLI、macOS app、IDE extension 共用這份設定）。在 Codex 裡說「peer review my changes」或「ask claude what it thinks about this design」即可。

手動等效設定：

```toml
# ~/.codex/config.toml
[mcp_servers.hostrunner]
command = "/Users/you/.local/bin/hostrunner"
tool_timeout_sec = 120
startup_timeout_sec = 20
```

---

## 設定

server 依下列順序決定設定檔：
1. `-config` 旗標指定的路徑。
2. `HOSTRUNNER_CONFIG` 環境變數。
3. 預設 `~/.config/hostrunner/config.yaml`。

**建議**放在 `~/.config/hostrunner/config.yaml`。GUI app 不繼承 shell 環境變數，shell profile 裡的 `HOSTRUNNER_CONFIG` 對 Claude Desktop 或 Codex app 啟動的 server 看不到。

從範例建立設定檔——**裡面的路徑是佔位符，請改成你自己的**：

```bash
mkdir -p ~/.config/hostrunner
cp examples/config.example.yaml ~/.config/hostrunner/config.yaml
```

```yaml
# 工具允許存取的主機絕對路徑；解析後落在這些根目錄之外的請求一律拒絕。
allowed_roots:
  - /Users/yourname/code

# run_command 可執行的 CLI（peer agent 不看這份清單，其 argv 由 server 固定）。
allowed_commands:
  - codex
  - claude
  - git

# 主機上單一指令（一次 review、一輪 chat、或一次 run_command）的最長時間。
# 必須帶單位（600s、10m）。job 在背景執行，所以可以遠大於 MCP client 的單次呼叫時限。
timeout: 600s

# 單次工具呼叫回傳的最大位元組數（超過會截斷並附註）。
max_output_bytes: 200000

# 是否即時把 peer CLI 輸出 tee 到 server 的 stderr（預設 true）。
# Claude Desktop 會收進 ~/Library/Logs/Claude/mcp-server-hostrunner.log。
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
    "hostrunner": {
      "command": "/usr/local/bin/hostrunner",
      "args": ["-config", "/Users/yourname/.config/hostrunner/config.yaml"]
    }
  }
}
```

把 `/usr/local/bin/hostrunner` 改成實際的二進位檔路徑（例如 `~/go/bin/hostrunner`），設定檔路徑也一併調整。`examples/claude_desktop_config.example.json` 有相同範例。

改完後重啟 Claude Desktop，MCP servers 清單應出現 **hostrunner**。

---

## 可用工具

| 工具 | 用途 |
|---|---|
| `review_start(folder, agent?, scope?, base?, commit?, prompt?)` | 由 peer agent 開始唯讀 review；回傳 `job_id`。 |
| `chat_start(folder, message, agent?, session_id?)` | 送一則訊息給 peer；回傳 `job_id`。帶上前一次回覆的 `session_id` 即可續接同一段對話。 |
| `job_status(job_id)` | long-poll（≤50 秒）取得 job 結果。 |
| `codex_review_start`、`codex_review_status` | 已棄用，等同 `review_start(agent=codex)` / `job_status`。 |
| `run_command(command, args, folder)` | 在主機資料夾執行允許清單中的 argv。 |

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

### `run_command`

在主機資料夾執行允許清單中的指令（argv，無 shell）。

| 參數 | 型別 | 必填 | 說明 |
|---|---|---|---|
| `command` | string | 是 | 執行檔名稱；必須在 `allowed_commands` 內 |
| `args` | string[] | 否 | 參數字串陣列 |
| `folder` | string | 是 | `allowed_root` 內的**主機**絕對路徑 |

**回傳：** exit code、耗時、stdout、stderr。

---

## Skills

`skills/` 下附三個 skill，採用 Claude（plugin）與 Codex（`~/.agents/skills/`）共用的 `SKILL.md` 格式：

- **`peer-review`** — 以 `review_start` / `job_status` 跑「編輯 → peer review → 編輯」循環；agent 中立，兩邊都能用。
- **`peer-chat`** — 以 `chat_start` / `job_status` 進行多輪討論，保留 `session_id`。
- **`codex-loop`** — 原本 Claude 端的循環，現在改用 `review_start(agent="codex")`。

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
- **唯讀 peer。** peer 的 argv 由 server 固定。codex 用 `codex review` / `codex exec --sandbox read-only --ignore-user-config`（OS 層沙箱）。claude 沒有沙箱，所以完全不給 shell：`-p --strict-mcp-config --setting-sources "" --permission-mode dontAsk --tools Read,Grep,Glob --allowedTools Read,Grep,Glob --disallowedTools Edit,Write,NotebookEdit,Bash`。claude review 的 `git status`/`git diff`/`git show` 由 hostrunner 自己執行（唯讀、有輸出上限）並嵌入 prompt。只有 `agents.<name>.command` 與 `extra_args` 來自設定；`allowed_commands` 只管 `run_command`。
- **不繼承權限、不遞迴。** `--setting-sources ""` 讓使用者自己的 Claude 權限規則與 hook 不會進到 peer（`--allowedTools` 是疊加在設定之上的，這樣它才是完整清單）；`--strict-mcp-config`（claude）與 `--ignore-user-config`（codex）讓 peer 啟動時沒有使用者的任何 MCP server，所以不會載入 hostrunner 自己。刻意不用 `--bare`：它會關閉 OAuth/keychain 認證，訂閱登入會失效。`-c mcp_servers={}` 也被否決：config 覆寫是合併而非取代。
- **`run_command` 指令允許清單。**
- **原生程序，非 Docker。** 容器化會失去主機 CLI 與登入狀態的存取。
- **僅主機路徑。** 以 `/sessions/` 開頭的路徑一律拒絕並給出提示。

---

## 開發

```bash
make build   # 建置
make test    # 測試
make vet     # vet
```

設計文件在 `docs/superpowers/specs/`。

---

## 授權

MIT — 見 [LICENSE](LICENSE)。
