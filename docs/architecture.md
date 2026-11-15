# 架構與擴充

目標是讓初學者從入口一路追到副作用，不需要先理解框架。程式按責任切分；四個工具由 `Registry` 查表分派，沒有 event bus 或多層繼承。

## 責任邊界

| 套件 | 擁有的責任 | 擴充方向 |
|---|---|---|
| `cmd/agent` | 進程邊界：signal、stdout／stderr、exit code、REPL | 輸入方式、session 存檔 |
| `internal/config` | CLI 與環境變數，一次驗證 | 設定檔、profile |
| `internal/agent` | 訊息歷史、回合、工具結果關聯 | context 壓縮、事件 callback |
| `internal/model` | 模型請求、完整性驗證、串流 | usage 統計、重試策略、provider 差異 |
| `internal/tools` | 參數驗證、四個工具、統一結果 | 改善工具內部 |
| `internal/tools/tools.json` | 模型看到的規格 | 參數說明與 schema |
| `internal/fsx` | 文字 I/O 與路徑 | 寫入策略（atomic write、backup） |
| `internal/execx` | 有界程序執行與取消 | 程序政策、外部取消協定 |

全部邏輯與測試都使用 Go 標準函式庫，沒有外部依賴。選擇這個組合，是因為 Go 已經把 TLS、JSON 與程序管理做成熟，教學重點可以放在 `context` 的傳遞、錯誤的分類，以及用 `httptest` 測整個 tool loop。

`tools.json` 用 `go:embed` 編進執行檔，仍然是獨立、可讀、可改的檔案。`NewRegistry` 會確認每個啟用的工具同時有 schema 與實作，缺少任何一邊就在啟動時失敗——協定與實作不一致不該等到對話中才發現。

## context 是一條線，不是一個參數

`ctx` 從 `main` 的 `signal.NotifyContext` 開始，往下到 `model.post`（HTTP 請求）、`execx.Run`（子程序）。三個層級各自再加一層限制：

```text
main     signal.NotifyContext        Ctrl-C 停止整個執行（單發模式）
repl     signal.NotifyContext        每一輪一個，Ctrl-C 只停止這一輪
agent    cfg.MaxTurns                回合上限（不是 ctx，是可讀的迴圈條件）
model    http.Client{Timeout: 120s}  單一請求上限
execx    context.WithTimeout(30s)    單一命令上限，並殺掉 process group
```

互動模式把範圍縮一層：`repl` 在每次請求前建立自己的 `signal.NotifyContext(ctx, ...)`，結束後立刻 `stop()`。所以 Ctrl-C 殺掉的是這一輪的 HTTP 請求與 bash 程序群組，session 繼續。判斷只看兩行：`errors.Is(err, context.Canceled) && session.Err() == nil`。

取消的傳播是自動的：`exec.CommandContext` 在 ctx 結束時呼叫 `Cmd.Cancel`，`proc_unix.go` 那裏對整個 process group 發 SIGKILL；`Cmd.WaitDelay` 保證被遺留的孫程序不會用 pipe 把 `Wait` 卡住。

## 失敗的分類

工具參數錯誤、檔案不存在、edit 歧義都回傳 tool message（`{"ok": false, ...}`），模型可以修正。HTTP 錯誤、無效模型格式、不完整 tool response 與回合上限則以非零碼結束 CLI，並用 `%w` 保留原始錯誤讓 `errors.Is`／`errors.As` 能判斷。

`model.ErrProtocol` 是一個 sentinel error。測試斷言的是錯誤的類別，不是文字：

```go
if !errors.Is(err, model.ErrProtocol) { ... }
```

同一輪工具依序執行，因此 write → read → edit 的依賴關係可預期。執行前先驗證整輪 tool call 的基本格式與唯一 ID（`model.validate`）；工具參數個別驗證並回傳錯誤。整輪不是交易，前面的成功操作不會因後面的失敗回滾。

HTTP request 沒有上限（由呼叫端控制），response 最多 4 MiB，每輪最多 16 個工具，設定最多 100 輪。模型歷史仍可能超過 context，第一版會回報服務錯誤，沒有自動摘要。HTTP client timeout 120 秒，程序層預設 30 秒（最多 300 秒）。

## 往可擴充 agent 前進

依真實需求逐項加入：

1. REPL 已在 `cmd/agent/repl.go`，`agent.RunTurn` 已經進出歷史。下一步是 `/save` 與 `/load`：歷史是 `[]model.Message`，寫成 JSON 就是 session 檔。中斷時目前是整体回滾，可改成只修剪最後一個不完整的 tool 區塊。
2. 在 `model` 保留 usage（串流已經讀取 `usage`），先量測 context，再設計裁切／摘要。
3. 把 `progress io.Writer` 換成很小的 callback interface，讓 TUI 或 logging 共用同一個 loop。
4. 若需要執行前審核，在 `Registry.Execute` 前加 policy hook，不把 UI 混入工具。
5. 需要平行工具執行時，用 `errgroup` 加上「同一輪不得有寫入」的限制；先保留依序執行為預設值。
6. provider 差異先交給 Bifrost；只有在 transport 真的有第二種需求時才加 adapter。

四個工具可以一直保留。大部分新功能應加入 session、UI、模型連線或執行政策層，讓核心維持小而穩定。

## 邊界與限制

workspace 檢查避免意外路徑越界，無法防禦硬連結、路徑競態或惡意並行程序。檔案直接覆寫，I/O 期間失敗可能留下部分內容，尚未提供 atomic write／backup。

bash 使用使用者環境與權限；process group 清理不等同容器隔離，另行建立 session 的子程序可能逃離 group。不支援互動程式、長期 background service 或外部取消協定。Windows 沒有 process group，逾時只殺直接子程序。

需要隔離或交易式寫入時，先加入獨立的執行環境／寫入策略，再保持四個工具介面不變。

## 與 Mojo 版同步的規則

兩個專案刻意維持相同的行為，方便對照：四個工具、同樣的預設值與上限、同樣的 `{"ok": ...}` 信封、同樣的回合上限與 workspace 檢查。刻意不同處只有三項：Go 版預設串流、`null` 參數在 Go 版會保留預設值，以及 Go 版有互動 REPL（Mojo 版只有單發）。

## 參考

- [Go: Effective Go](https://go.dev/doc/effective_go)
- [context 套件](https://pkg.go.dev/context)
- [os/exec: Cmd.Cancel 與 WaitDelay](https://pkg.go.dev/os/exec#Cmd)
- [errors: Wrapping 與 Is／As](https://go.dev/blog/error-handling-and-go)
- [httptest](https://pkg.go.dev/net/http/httptest)
- [go:embed](https://pkg.go.dev/embed)
- [Bifrost Request Options](https://docs.getbifrost.ai/providers/request-options)
- [Bifrost OpenAI SDK 相容介面](https://docs.getbifrost.ai/integrations/openai-sdk/overview)
