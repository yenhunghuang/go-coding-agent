# Go Coding Agent

![Test](https://github.com/yenhunghuang/go-coding-agent/actions/workflows/test.yml/badge.svg)

用 **Go 標準函式庫** 學習 coding agent：一個看得完的 tool loop、四個工具，以及 Go 真正在用的那套習慣——`context`、error 當值、`httptest` 與 table-driven test。

零外部依賴。CLI、HTTP、JSON、子程序與逾時全部由 Go 標準函式庫處理；沒有 framework、沒有 registry、沒有 event bus。

這個專案是 [mojo-coding-agent](https://github.com/yenhunghuang/mojo-coding-agent) 的 Go 對照版：同樣的四個工具與同一個 tool loop。Mojo 版必須自己寫 JSON parser、用 curl 處理 TLS、用 POSIX FFI 管程序；Go 有這些東西，所以省下来的力氣拿去學 `context` 取消、SSE 串流、程序群組清理，以及 Go 的測試文化。

## 開始

需要 [Go](https://go.dev/dl/) 1.23 以上。

```bash
git clone https://github.com/yenhunghuang/go-coding-agent.git
cd go-coding-agent
go test ./...                 # 離線測試，不需要 Bifrost 或 API key
go run ./cmd/agent --help
```

先啟動並設定好 [Bifrost gateway](https://docs.getbifrost.ai/quickstart/gateway/setting-up)，在 gateway 設定 provider 憑證，再設定 client：

```bash
export OPENAI_BASE_URL="http://localhost:8080/v1"
export OPENAI_MODEL="openai/your-tool-capable-model"
# Gateway 需要驗證時才設定；provider key 通常由 Bifrost 管理。
export OPENAI_API_KEY=""

go run ./cmd/agent -tools read "讀取 README.md，說明這個專案的架構"
go run ./cmd/agent -workspace /absolute/path/to/your/project \
  "讀取程式，修正問題，並用 bash 執行相關測試"
```

stdout 只有最終答案；`[tool] ...`、token 用量與串流增量都寫到 stderr，所以管線只看到一個結果。

將 model 換成你在 Bifrost 設定的 `provider/model`。直連其他 OpenAI-compatible 服務也可，只需調整 base URL、model 與 API key。Base URL 包含服務所需前綴（例如 `/v1`）；程式只附加 `/chat/completions`。模型必須支援 function tool calling。`.env.example` 是 shell 設定範例，程式不會自動載入 `.env`。

## 先讀哪裡

```text
cmd/agent/main.go     CLI → configuration → agent
internal/config/      少量明確的 CLI 參數與環境變數
internal/agent/       訊息歷史 → 模型 → 工具 → 下一輪
internal/model/       OpenAI 請求、完整性檢查、SSE 串流
internal/tools/       read / write / edit / bash
    └── tools.json    模型看到的四個工具規格（go:embed）
internal/fsx/         文字 I/O 與 workspace 路徑檢查
internal/execx/       子程序、有界輸出、逾時與程序群組
```

從 [逐步教學](docs/learning.md) 開始，再讀 [架構與擴充](docs/architecture.md)。先看核心流程，再進入 `fsx` 與 `execx` 的基礎設施。每一步都使用同一套程式，避免維護多份逐漸失同步的 agent。

## 四個工具

| 工具 | 行為 | Go 練習重點 |
|---|---|---|
| `read` | UTF-8 文字、1-based 行號、行數與輸出上限 | `strings`、`range` 產生 rune、`io.LimitReader` |
| `write` | 建立／完整覆寫檔案；父目錄須已存在 | struct tag 參數驗證、`os` 錯誤 |
| `edit` | 舊文字必須唯一匹配才替換；新文字可為空 | `strings.Index`、歧義檢查、先驗證再修改 |
| `bash` | 工作目錄內執行命令；回傳輸出、退出碼、逾時狀態 | `os/exec`、`context.WithTimeout`、`sync.Mutex` |

- `read` 預設從第 1 行讀取 200 行，最多回傳 16,000 個 Unicode code points（Go 的 rune）。`truncated` 表示後續內容省略；超出 EOF 回傳空內容。行分隔以 LF 為準。
- 檔案工具最多處理 1 MiB UTF-8 文字，拒絕 NUL 與無效 UTF-8。`edit` 保留未修改部分，包括 CRLF。
- `edit` 的零次、多次與重疊匹配都不會寫入；空的 `old_text` 不接受。`write` 不自動建立目錄，可透過 bash 建立。
- `bash` 預設逾時 30 秒（最多 300），保留 16,000 bytes 輸出（最多 100,000）。超出部分持續 drain 但丟棄，避免子程序因 pipe 滿而卡住。
- bash 合併 stdout／stderr。非零 `exit_code` 表示命令失敗；被 signal 終止則是負的 signal number。逾時另回傳 `timed_out: true`。
- 工具錯誤以 `{"ok": false, "error": "..."}` 傳回模型；模型連線／協定錯誤則停止程式。最多 20 次模型請求，可用 `-max-turns 40` 調整。

這是具有檔案修改與 shell 權限的本機 agent。檔案工具檢查 workspace 路徑與 symlink，但 **bash 不是 sandbox**，仍可存取其他路徑、網路與環境變數；請在可信任的工作環境使用。寫入直接覆寫，沒有交易、備份或並行編輯保護，適合在 Git 工作目錄操作。

## 與 Mojo 版的對照

同一個 agent，兩套語言的標準做法。讀 Mojo 版再讀這裡，會看到 Go 把哪些難題變成標準函式庫的工作。

| 主題 | Mojo 版 | Go 版 |
|---|---|---|
| JSON | 自己寫 recursive descent parser | `encoding/json`、struct tag、`json.RawMessage`、`DisallowUnknownFields` |
| HTTP／TLS | 叫 `curl` 子程序，設定寫進私人暫存檔 | `net/http` + `http.NewRequestWithContext` |
| 子程序 | POSIX FFI：`fork`／`poll`／`waitpid` | `os/exec` + `exec.CommandContext` |
| 逾時與取消 | `curl --max-time`、手動 kill process group | `context.WithTimeout`、`Cmd.Cancel`、`Cmd.WaitDelay` |
| 錯誤處理 | `raises` 與 `try/except` | error 當值、`fmt.Errorf("%w")`、`errors.Is`／`errors.As` |
| 字串 | bytes vs code points，`codepoint_slices()` | UTF-8 bytes vs rune，`range` 不會切壞中文字 |
| 平台邊界 | 只支援 POSIX | build constraints：`proc_unix.go`／`proc_windows.go` |
| 測試 | Mojo 寫的低階 mock HTTP server（raw socket） | `httptest.Server`、`t.TempDir()`、table-driven、`go test -race` |
| 串流 | 列為未來工作 | 預設開啟，SSE 增量組回同一個驗證規則 |

行為上有一處刻意不同：`{"limit": null}` 在 Go 版會保留預設值（`encoding/json` 對 `null` 是「不改动」），Mojo 版則視為參數錯誤。

## 開發

```bash
make vet         # go vet ./...
make fmt         # gofmt -s -w .
make test        # go test ./...
make race        # go test -race ./...
make cover       # 覆蓋率報告
make build       # build/agent
./build/agent --help
```

測試會編譯真正的 Go 程式，驗證檔案、路徑、CLI、子程序清理，並用 `httptest.Server` 假模型讓真正的 HTTP 呼叫串起四個工具與多輪訊息。CI 在 Linux x86-64、macOS 與 Windows 執行相同測試，並檢查 `gofmt` 與 `go vet`。

第一版採用單一任務、依序執行工具。REPL、session 持久化、context 壓縮、自動重試與 plugin 系統可沿既有邊界逐步加入。

MIT License.
