# 從四個工具學 Go agent

先執行 `go test ./...`。測試用 `httptest.Server` 假模型，不需要 API key 就能驗證。實際對話先依 README 設定 Bifrost。

## 第一步：read 與 tool loop

```bash
go run ./cmd/agent -stream=false -tools read "讀取 internal/agent/agent.go，說明模型如何取得工具結果"
```

依序閱讀 `cmd/agent/main.go` → `internal/agent/agent.go` → `internal/model/client.go` → `internal/tools/file.go` 的 `readTool`。

每輪只有三件事：

1. 把完整 `messages` 與工具 schema 傳給模型。
2. assistant 有 `tool_calls` 時，先保存該 assistant message，再依序執行工具。每個結果帶上對應的 `tool_call_id`。
3. 把 tool message 加回歷史，再問模型。沒有工具呼叫的完整文字回應才是最終答案。

`-tools read` 同時限制模型看到的規格與 dispatcher 允許的執行，因此模型即使要求 write，也只會得到 `{"ok":false,"error":"tool is not enabled: write"}`。

Go 的重點在這裡：**error 是值**。`fsx.ReadText` 回傳 `(string, error)`，呼叫端決定要不要往上傳。`agent.Run` 把工具錯誤包成 tool message，把協定錯誤直接 return：

```go
if err != nil {
    return "", fmt.Errorf("turn %d: %w", turn, err)
}
```

`%w` 是 wrap，不是字串拼接。測試可以用 `errors.Is(err, model.ErrProtocol)` 判斷「這是模型的錯」，不必比對錯誤文字。

`-tools read` 只影響 `Registry`，不影響 tool loop 本身；`Registry.Execute` 永不回傳 error，因為工具失敗是給模型的資訊，不是給程式的異常。

注意 Go 的慣例：`ctx context.Context` 永遠是第一個參數；`out`／接收者寫法；導出與未導出（`internal/` 之下的套件只有這個模組能用）。

練習：改變 `start_line`、`limit` 與 `max_output`，觀察空檔、結尾換行、中文、超出 EOF 的結果。對照 `internal/tools/tools_test.go` 的 `TestReadLines`。

## 第二步：write，再加入 edit

```bash
mkdir -p /tmp/go-agent-practice
go run ./cmd/agent -stream=false -workspace /tmp/go-agent-practice -tools read,write,edit \
  "建立 hello.txt，內容為 hello Go，讀取它，再將 hello 精確改成 welcome"
```

閱讀 `internal/tools/tools.go` 的 `decode` → `internal/fsx/fsx.go` 的 `WriteText` → `editTool.Call`。

參數驗證用 struct tag，而不是手寫 key 比對：

```go
var args struct {
    Path    string `json:"path"`
    Content string `json:"content"`
}
```

`decode` 開啟 `DisallowUnknownFields`，所以模型多給一個 key 就會被明確拒絕，而不是被忽略。整輪 tool call 的格式在 `model.validate` 檢查，單一工具的參數在工具自己檢查。

這裡採用「先驗證，再修改」：零匹配、多匹配、空舊文字都會在覆寫前失敗；只有唯一匹配才呼叫 `strings.Replace` 與 `fsx.WriteText`。`strings.Index(content[first+1:], old)` 從下一個 byte 再找一次：`aaa` 裡的 `aa` 互相重疊，仍然是歧義。刪除文字就是 `new_text=""`。

路徑檢查在 `fsx.ResolvePath`：先 `filepath.EvalSymlinks`，再要求結果以 workspace 為前綴。檔案不存在時改解析父目錄，所以 `link/outside.txt` 這種「連結到外面」的寫法也會被擋掉。

練習：將檔案改成兩行相同文字，要求模型只修改其中一行。模型應透過更長的 `old_text`（加入上下文）消除歧義。`TestEditValidatesBeforeWriting` 會確認失敗後檔案仍維持原內容。

## 第三步：bash、context 與程序群組

```bash
go run ./cmd/agent -stream=false -workspace /tmp/go-agent-practice \
  "建立 hello.go，main 印出 hello，執行 go run hello.go 並驗證輸出"
```

閱讀 `internal/tools/bash.go` 的 `Call`，再看 `internal/execx/execx.go` 與 `proc_unix.go`。整個程序流程是 Go 標準函式庫加上一層 syscall：

```text
exec.CommandContext(bash -c)
  ├─ configure: Setpgid → 自成一組
  ├─ Stdout/Stderr → 同一個 bounded writer（mutex 保護）
  └─ ctx 到期 → Cmd.Cancel: kill(-pgid, SIGKILL) → WaitDelay 收尾
```

`context` 是 Go 版跟 Mojo 版最大的差別。同一個 `ctx` 從 `main` 一路傳下去：

- `signal.NotifyContext` 讓 Ctrl-C 取消整個執行；
- `http.NewRequestWithContext` 讓進行中的模型請求立刻中斷；
- `execx.Run` 用 `context.WithTimeout` 讓單一命令有逾時，並殺掉整個 process group。

這取代了 Mojo 版用 `curl --max-time`、`poll` 與手動 `waitpid` 拼出來的逾時邏輯。

| 狀態 | 如何判斷 |
|---|---|
| 正常完成 | `ExitCode == 0` 且 `TimedOut == false` |
| 測試失敗 | `ExitCode != 0`；模型仍可讀 output 修正程式 |
| 被 signal 殺掉 | `ExitCode < 0`，值是 signal number |
| 超時 | `TimedOut == true`；已清理 process group |

`bounded.Write` 永遠回報「全部寫完」。回報短寫入會讓子程序看到寫入錯誤，pipe 滿時可能卡死；這裡的規則是持續 drain、丟棄超出部分。stdout 與 stderr 由兩個 goroutine 寫入同一個 buffer，所以需要 `sync.Mutex`——這是 Go 併發最常見的入門案例，`go test -race` 會幫你抓到漏掉的鎖。

平台差異用 build constraints 處理，而不是 `if runtime.GOOS`：

```go
//go:build unix
```

`proc_unix.go` 用 `Setpgid` 與 `syscall.Kill(-pid, SIGKILL)`；`proc_windows.go` 只能殺直接子程序。同一套工具介面，兩種實作。

## 第四步：串流（Mojo 版的未來工作，Go 版預設開啟）

```bash
go run ./cmd/agent -tools read "用三句話說明 internal/model/stream.go"
```

`internal/model/stream.go` 讀 SSE：每一行 `data: {...}` 是一個增量，`data: [DONE]` 結束。三個 Go 細節值得注意：

1. 用 `bufio.Reader.ReadString`，不是 `bufio.Scanner`：Scanner 有單行長度上限，而一個 tool call 的 delta 可能很長。
2. 增量要按 `index` 組回 `ToolCall`：`id`、`name` 通常只在第一個 chunk，`arguments` 會被切成好幾段。
3. `onDelta` 是 callback，不是 channel。單一 reader goroutine 加一個 callback，讓取消只有一個來源（`ctx`），不必寫 `select`。

串流與非串流最後都進同一個 `validate`：沒有 choices、tool call 沒有 id、id 重複、`finish_reason` 不是 `tool_calls`／`stop`，兩條路徑都一樣拒絕。這是把「傳輸」與「協定」分開的實際好處。

## 第五步：REPL，以及一段真正的對話

```bash
go run ./cmd/agent -workspace /tmp/go-agent-practice   # 不給 prompt 就是互動模式
```

先問一個問題，再問第二個「那它呢？」——模型會記得上一輪。這背後的改動只有三個，每個都是 Go 的必修課。

**1. 把狀態搬到參數與回傳值。** `agent.Run` 拆成 `RunTurn`：歷史從參數進、從回傳值出，函式本身無狀態。

```go
func RunTurn(
	ctx context.Context,
	cfg *config.Config,
	client *model.Client,
	registry *tools.Registry,
	messages []model.Message,
	progress io.Writer,
) (string, []model.Message, error)

func Run(...) (string, error) {   // 單發模式只是它的一個 wrapper
    answer, _, err := RunTurn(ctx, cfg, client, registry, InitialMessages(cfg.Prompt), progress)
    return answer, err
}
```

`RunTurn` 第一行是 `messages = slices.Clone(messages)`。這是 Go 最容易踩的坑之一：`append` 可能會寫進呼叫端的 backing array，於是「你傳進去的 slice」和「你拿回來的 slice」共用同一塊記憶體。測試直接驗這件事：

```go
if &history[0] == &start[0] {
    t.Error("RunTurn appended into the caller's slice")
}
```

**2. 最終答案也要進歷史。** 單發模式看不出差別，但如果 assistant 的最後一則訊息不存進歷史，第二輪就看不到自己剛說了什麼。這是做多輪時第一個會發現的 bug。

**3. 兩個 context，範圍不同。**

```go
turnCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)  // 每一輪一個
answer, next, err := agent.RunTurn(turnCtx, ...)
stop()
```

`ctx` 是 session，`turnCtx` 是這一輪。Ctrl-C 只取消 `turnCtx`，所以卡住的 bash 或很慢的模型請求會停，但提示字元回來。對照單發模式：那裏只有一個 `signal.NotifyContext`，因為整個程序就是這一輪。

中斷會留下一個不能用的歷史：assistant 要求了 tool，但 tool 結果沒有全部進來，下一次請求會被 API 拒絕。所以 REPL 回滾到上一個完整交換：

```go
if interrupted(err, ctx) {
    history = append(before, model.Message{Role: "user", Content: "(the previous request was interrupted)"})
}
```

`interrupted` 只有兩行，但它是整個中斷策略，所以有自己一張 table test：turn 被取消而 session 還活著 → 繼續；session 本身被取消 → 結束。

中斷這條路徑靠 signal，測試不好模擬，所以手動驗證（注意：在 script 裡用 `&` 背台跑会被继承 `SIG_IGN`，Go 的 `signal.Notify` 對它無效，要先 `set -m`）：

```bash
mkfifo /tmp/in
./build/agent -tools bash -stream=false < /tmp/in &   # 先 set -m
printf 'run: sleep 25 then echo done\n' >&3
kill -INT $!                                          # 應該看到 (interrupted) 與新的 you>
```

看到 `(interrupted)` 代表 `turnCtx` 被取消、bash 的 process group 被 SIGKILL，而 session 沒死。

練習（由易到難）：

1. `/save FILE` 與 `/load FILE`。`model.Message` 已經有 json tag，`json.MarshalIndent(history)` 就是 session 檔。
2. 寫 `truncateIncomplete(messages)`，只丟掉最後一個不完整的 assistant/tool 區塊，保留已經完成的工具結果，取代現在的整体回滾。
3. 把 `bufio.Scanner` 換成自帶行編輯的輸入（方向鍵、歷史列），注意這需要把終端切到 raw mode。

## 再深入基礎設施

`internal/model/client.go` 有兩件事值得停下來看：

- `io.LimitReader(resp.Body, c.MaxResponse+1)`：多讀一個 byte 就足以分辨「剛好到上限」與「超過上限」，不必把整個 response 進記憶體。
- `HTTPError` 只帶 status，不帶 response body。gateway 的錯誤 body 可能含內部資訊或憑證，不該進到 log 或模型歷史。

`internal/fsx/fsx.go` 示範 `io.LimitReader` 的另一种用法，以及 `utf8.Valid` 與 NUL 檢查：二進位檔不進 context，避免模型看到一堆亂碼還繼續改寫。

測試部分先讀 `internal/agent/agent_test.go`：`httptest.Server` 回放腳本，並記錄每次請求，所以測試能斷言「模型最後真的看到這 4 個 tool_call_id」。`t.TempDir()` 每個測試一個工作區，結束自動清掉。`internal/execx/execx_test.go` 則用 `(sleep 2; touch escaped) & wait` 證明 process group 真的被殺掉。

下一步先做 session 持久化（`/save`、`/load`），再加 context 用量統計。每次只改一個邊界，維持四個工具的介面。詳見 [架構與擴充](architecture.md)。
