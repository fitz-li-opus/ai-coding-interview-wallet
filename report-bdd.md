# 測試報告

> 規格是 [`wallet/spec/`](./wallet/spec)，需求編號在 [`plan.md`](./plan.md)，設計決策在
> [`plan-wallet-go-postgres.md`](./plan-wallet-go-postgres.md)。
> 互動式報告：[`reports/bdd-all.html`](./reports/bdd-all.html)（全套）、
> [`reports/bdd-minimum.html`](./reports/bdd-minimum.html)（README 最小需求）。

---

## 1. 結論

| 項目 | 結果 |
|------|------|
| 規格驗證 `realspec validate` | **11 個檔案全 PASS、exit 0**（10 個 feature + `format.yml`） |
| 未使用的 step | **0**（19 個 step 全部至少被一條 scenario 使用，沒有用 `--allow-unused-steps` 規避） |
| BDD scenario | **72 條／展開後 83 次執行，83／83 通過** |
| BDD step | **707／707 通過** |
| 失敗 | **0** |
| 服務單元測試 | `go test ./...` 全過（兩個 module） |
| `services/` 的 diff | **空**（`git diff --stat origin/main HEAD -- services/` 無輸出） |

## 2. 逐 tag 結果

tag 與 feature 檔是一對一，所以下表的聯集就是整套、每條剛好一次。全套**沒有重跑**：報告由各 tag 自己那一次執行的結果合併而成，省下約 11 分鐘與 249 個容器。

| Tag | 執行次數 | Step | 重跑 |
|-----|--------:|-----:|------|
| `@spend` | 16 | 133 | |
| `@credit` | 10 | 85 | |
| `@dedupe` | 10 | 102 | 再跑一次仍全綠 |
| `@signature` | 8 | 57 | |
| `@ordering` | 8 | 71 | 再跑一次仍全綠 |
| `@wallets` | 7 | 40 | |
| `@observability` | 7 | 44 | |
| `@resilience` | 6 | 64 | 連續三次全綠 |
| `@reconcile` | 6 | 60 | 連續兩次全綠 |
| `@refund` | 5 | 51 | |
| **合計** | **83** | **707** | |

`@minimum`（README 最小需求，7 條，跨 credit／wallets／spend）單獨執行：**7／7、54／54 step、wall clock 40.3 秒**（併發 4）。

## 3. README 最小需求的那 7 條

| # | Scenario | 斷言 |
|---|----------|------|
| 1 | A succeeded payment credits the wallet named in the event | 真 provider 建立 500 → 餘額 500、1 筆 `credit` 分錄、1 列 inbox，**而且從 provider 自己的紀錄讀出「恰好 1 次被接受、0 次被拒絕」** |
| 2 | A payment for a user this service has never seen creates the wallet | 錢包被建立、分錄的 `payment_id` 等於 provider 回傳的 id（context 變數比對，不是寫死字串） |
| 3 | A wallet reports the credits that arrived for it | 直接 seed 後 `GET` 回 500（讀與寫分開測，讀的失敗不會被寫的成功掩蓋） |
| 4 | Credits that arrive from the payment provider are visible in the balance | 完整路徑：付款 → 簽章 webhook → 入帳 → `GET` |
| 5 | Spending deducts the credits and answers with the new balance | 餘額 400、1 筆 `spend -100`、該 user 的分錄**恰好 2 列** |
| 6 | Spending the entire balance is allowed and lands exactly on zero | 邊界在「等於」這一側合法 |
| 7 | **Insufficient balance is refused and writes nothing** | `422 INSUFFICIENT_BALANCE`、`kind='spend'` 的分錄 **0 列**、餘額仍是 500 |

第 7 條是整個專案選 RealSpec 的理由：只斷言狀態碼的測試會在「先扣款再回 422」時照樣通過，也會在「扣了一半」時照樣通過。這裡的 `0 rows` 與 `balance = 500` 由**不是待測系統**的 PostgreSQL 回答。

## 4. 這 83 次執行順帶證明的事

| 事情 | 為什麼它被證明了 |
|------|----------------|
| 服務綁 `0.0.0.0` 而非 `127.0.0.1`（R-06） | 沒有任何 step 能問「你綁在哪」。但 provider 在**另一個容器**裡，它送得到我們就說明綁對了 |
| 簽章演算法與 provider 一致 | 入帳路徑走的是真的 provider，它簽、我們驗過才入帳。簽錯就不會有餘額——而且規格裡從頭到尾沒有出現任何雜湊值 |
| `2xx` 代表已 commit | provider 的投遞紀錄顯示 1 次接受、0 次拒絕；若在 commit 之前就回 2xx，停掉 DB 那條會抓到 |
| 去重鍵是事件 id 而不是付款 id | `@refund` 斷言同一筆付款的 inbox 有 **2 列**（succeeded + refunded），而分錄只沖一次 |
| 餘額等於分類帳總和 | 多條 scenario 同時斷言 `wallets.balance` 與 `wallet_entries` 的內容與列數 |
| 沒有固定秒數的等待 | 非同步只用 `background work has settled`（條件式）；registry 裡根本沒有「等 N 秒」這種句子 |

## 5. 執行環境與成本（實測）

每個 scenario 自己一套三個容器：`postgres:17-alpine`、wallet、**題目原本的 payment provider**（一行都沒改，只設 `WEBHOOK_URL`／`WEBHOOK_SECRET`／`DUPLICATE_RATE`）。生命週期全交給 testcontainers。`DUPLICATE_RATE=0`：重複投遞由 step 決定性製造，不靠 0.3 的機率——否則「只入帳一次」有 70% 的時候根本沒被測到。

成本模型（從 83 次執行回歸）：

```
serial ≈ runs × 13.5s  +  settles × 2.9s
```

- **13.5s／scenario** 是容器的起與拆（拆除約 10.6s），**與那條 scenario 在做什麼無關**。
- **2.9s／settle** 是等 provider 真的送達（它自己有約 1 秒發送延遲）＋ 1.7 秒靜默窗。
- 其餘斷言全是毫秒級：`@minimum` 的 7 次執行裡，**純斷言 8.4 秒、容器生命週期 96.6 秒**。

全套：serial 約 1300 秒，併發 2 約 11 分鐘，併發 4 約 5.5 分鐘。provider 的 `[0,1,2,4,8,16]` 秒重試序列**從來沒有被等完**——`@reconcile` 用暫停 provider 來排演「它放棄了」，`@resilience` 用 `resend`（單次投遞）拿到精確次數。

## 6. 怎麼跑

```bash
# 1 · 規格是否成形（唯一不花錢的一關，所以先跑）
cd wallet && npx realspec validate --format spec/bdd/format.yml spec/bdd/api/*.feature

# 2 · scenario
cd test/bdd
BDD_TAGS=@minimum go test ./...        # README 最小需求，7 條
BDD_TAGS=@signature go test ./...      # 單一 tag
BDD_CONCURRENCY=4 go test ./...        # 全套，約 5.5 分鐘

# 3 · HTML 報告
BDD_FORMAT='pretty,cucumber:../../../reports/run.json' go test ./...
python3 tools/cucumber_html.py ../../../reports/run.json ../../../reports/run.html
```

godog 沒有 HTML formatter（v0.16 只註冊 `pretty`／`progress`／`cucumber`／`junit`／`events`），而 Cucumber 官方的 HTML formatter 吃的是 godog 不會輸出的 Messages NDJSON。所以 `tools/cucumber_html.py` 自己渲染，零依賴、單一檔案——替代方案是為一個 Go 專案掛上一棵 Node 依賴樹。

## 7. 過程中抓到的三個真 bug

都不是「程式碼沒照規格做」，而是**只有真的把它跑起來才會現形**的東西。

| Bug | 位置 | 問題 |
|-----|------|------|
| 幂等回放不是逐位元相同 | 服務 | `idempotency_keys.response_body` 是 `jsonb`，存的是**值**不是 bytes，所以寫進去的 `{"user_id":…,"balance":…}` 讀回來可能鍵序顛倒。修法是用產生原回應的同一個 marshaller 重新 render：**byte-identity 是「重新產生」的性質，不是「儲存」的性質** |
| outline 的數值參數變成字串 | harness | JSON docstring 必須在 Examples 代換**之前**就能解析，所以數值位置得寫 `"<amount>"`，代換後 step 收到 `"500"`，真 provider 嚴格檢查 `type(amount_cents) is int` 而拒絕——**它拒絕得對**。修法是只對對手方契約宣告為整數的欄位重新轉型，不是「每個看起來像數字的字串」 |
| 併發重送等錯了目標 | harness | 重送 step 把等待目標設成「觀察到的 + 我這次的份」而非累加，四份並發重送會各自只等到第一次嘗試就返回——那條競態 scenario 會因為**運氣**而綠 |

另外修掉一個**報告本身在說謊**的問題：godog 的 cucumber formatter 把 `Before`／`After` hook 的時間記到相鄰的 step 上，而我們的 hook 每個 scenario 要起／拆三個容器。所以修正前的「最慢 step」表會告訴讀者「一句 0 列的 SQL 查詢要 10.6 秒」。現在報告把每條 scenario 的首尾 step 排除在「最慢斷言」之外，另列一張容器生命週期表並寫明原因。

## 8. 已知沒有被斷言的需求

誠實記下來，免得之後誤以為測過了。完整清單在
[`plan-wallet-go-postgres.md` §16.2](./plan-wallet-go-postgres.md)。最值得知道的兩條：

- **「provider 對第二次退款回 `409`」目前無斷言**：呼叫對手方的那個 step 預期 2xx，承載不了對方的錯誤回應。那條 scenario 被**刪掉**，而不是改寫成一個「會過但什麼都沒斷言」的版本。
- **「webhook 必須在 5 秒內處理完」測不到**：registry 沒有計時的 step，硬加會讓套件變成 flaky 的效能測試。它由 `ingest.Apply` 自己的 4 秒上限（低於 provider 的 5 秒逾時）與單元測試守住。
