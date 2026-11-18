# Credit Wallet 需求規格

> 本文件只做一件事：把 `README.md` 的題目變成**可追溯、可驗證**的需求條目。
> 每一條都標明是「題目原文」、「由 `services/payment-provider/provider.py` 推導」還是「題目沒說、必須自己決定」。
>
> 設計決策、架構圖、流程圖、時序圖、實作與測試計畫在 [`plan-wallet-go-postgres.md`](./plan-wallet-go-postgres.md)。
> 系統的權威描述最終會是 `wallet/spec/`（RealSpec）；本文件與 `spec/` 若有出入，以 `spec/` 為準。
>
> 本文件不含程式碼。

---

## 0. 目錄

1. 背景與目標
2. 環境
3. Payment provider API（唯讀、不可修改）
4. Webhook 契約
5. 必做需求（R）
6. 交付需求（R-2x）
7. 規則與限制（R-3x）
8. 由 provider 原始碼推導的隱含需求（I）
9. 題目未定義、必須自己決定的事（U）
10. Bonus 空間（B）
11. 時間配置
12. 需求追溯表
13. 非目標

---

## 1. 背景與目標

使用者透過第三方金流購買 credits，並在產品內消費 credits。付款成功時，金流商以 **webhook** 通知我們——**那一刻才是 credits 到帳的時刻**。

要實作的是一個 wallet service：接收 webhook 入帳、查詢餘額、扣款。

| 項目 | 內容 | 來源 |
|------|------|------|
| 目標 | 一個 credit wallet service，監聽 **8080** | 題目原文 |
| 工作目錄 | `wallet/` | 題目原文 |
| 技術棧 | 任意語言、任意儲存 | 題目原文 |
| 建置時間 | 25 分鐘（全程 45 分鐘） | 題目原文 |
| 評分重點 | **怎麼與 AI 協作**，不只是最終結果 | 題目原文 |
| 加分 | 最小需求之外，任何「production-grade 付款與 credit 系統該有的東西」 | 題目原文 |

---

## 2. 環境

| 服務 | 位址 | 說明 |
|------|------|------|
| payment-provider | `http://localhost:8090` | 題目提供的 mock 金流商；單一 Python 檔、無第三方依賴（Python 3.8+） |
| wallet（要實作的） | `http://0.0.0.0:8080` | webhook 進入點 `POST /webhooks/payments` |

- provider 以 `python3 services/payment-provider/provider.py` 啟動，或 `docker compose up --build`。
- 走 Docker 時 provider 從 `host.docker.internal:8080` 找我們，所以 wallet **必須監聽 `0.0.0.0`**，不是 `127.0.0.1`。
- `GET /health` 可確認 provider 活著。

---

## 3. Payment provider API（唯讀、不可修改）

| Method & path | 行為 | 回應 |
|---------------|------|------|
| `POST /payments` | 建立付款，1 cent = 1 credit；約 1 秒後送出 `payment.succeeded` | `201` + payment 物件 |
| `POST /payments/{id}/refund` | 退款；隨後送出 `payment.refunded` | `200` + payment 快照 |
| `POST /events/{id}/resend` | 重送某個事件（等同 provider 後台的「Resend」按鈕） | `202` |
| `GET /events` | 列出所有事件與每次投遞嘗試 | `200` |
| `GET /health` | 健康檢查 | `200` |

payment 物件欄位：`id`、`user_id`、`amount_cents`、`credits`、`status`。

---

## 4. Webhook 契約

provider 以 `POST`、`Content-Type: application/json` 送到 `/webhooks/payments`。

| 欄位 | 型別 | 說明 |
|------|------|------|
| `id` | string | 事件 id，`evt_` 前綴 |
| `type` | string | `payment.succeeded` 或 `payment.refunded` |
| `created` | int | 事件建立時間（unix 秒） |
| `data.payment.id` | string | 付款 id，`pay_` 前綴 |
| `data.payment.user_id` | string | 錢包主體 |
| `data.payment.amount_cents` | int | 金額（分） |
| `data.payment.credits` | int | 要入帳／沖回的 credits（= `amount_cents`） |
| `data.payment.status` | string | `succeeded` 或 `refunded` |

送達保證（題目原文）：

| 性質 | 內容 |
|------|------|
| 至少一次 | 非 2xx、或 5 秒內沒有回應，就以 backoff 重送 |
| 可能重複 | 同一個事件可能被送達多次 |
| 不保證順序 | 事件不保證依序抵達 |

簽章（題目原文）：

| 項目 | 內容 |
|------|------|
| header | `Opus-Signature: t=<unix timestamp>,v1=<hex signature>` |
| 演算法 | `HMAC-SHA256(secret, "<t>." + raw request body)` |
| 簽章密鑰 | `whsec_interview_test`（provider 端為環境變數 `WEBHOOK_SECRET`） |

---

## 5. 必做需求（R）

| Id | 需求 | 來源 |
|----|------|------|
| **R-01** | `POST /webhooks/payments` 接收 provider webhook | 題目原文 |
| **R-02** | 付款成功的事件，把它的 `credits` 加進該 `user_id` 的錢包 | 題目原文 |
| **R-03** | `GET /wallets/{user_id}` 回傳 `user_id` 與 `balance` | 題目原文 |
| **R-04** | `POST /wallets/{user_id}/spend`，body 帶 `amount`，扣款並回傳新餘額 | 題目原文 |
| **R-05** | 餘額不足時回 **4xx**，而且**什麼都不改變** | 題目原文 |
| **R-06** | 服務監聽 8080、綁 `0.0.0.0` | 題目原文（Docker 章節） |

R-05 的「什麼都不改變」是這份題目裡唯一一條明寫的**負面後置條件**，也是本專案選擇 RealSpec 的直接理由：一個只斷言狀態碼的測試會在「先扣款再拒絕」時照樣通過。

---

## 6. 交付需求（R-2x）

| Id | 需求 | 來源 |
|----|------|------|
| **R-20** | 可執行的程式碼 | 題目原文 |
| **R-21** | 一份 curl demo：向 provider 建立付款 → 顯示錢包裡的 credits → 花掉它們 | 題目原文 |
| **R-22** | 能說明設計決策與取捨 | 題目原文 |
| **R-23** | 在自己 fork 上工作，完成後開 PR 回 `main` 並填 PR template | 題目原文 |
| **R-24** | PR template 要填：姓名、日期、AI 工具、語言、儲存、做了什麼、功能勾選、已知缺口、如何執行、demo | `.github/PULL_REQUEST_TEMPLATE.md` |

---

## 7. 規則與限制（R-3x）

| Id | 限制 | 來源 |
|----|------|------|
| **R-30** | **不得修改** `services/` 底下任何程式碼 | 題目原文 |
| **R-31** | 所有自己的程式碼寫在 `wallet/` | 題目原文 |
| **R-32** | 面試官全程看螢幕；重點是與 AI 協作的過程 | 題目原文 |
| **R-33** | 面試官會在 demo 後對服務跑幾項檢查 | 題目原文 |

R-30 有一個重要的推論：provider 的行為**不能為了讓測試好寫而改**。但它的行為由環境變數參數化（`WEBHOOK_URL`、`WEBHOOK_SECRET`、`DUPLICATE_RATE`、`PORT`），**調整環境變數不是修改程式碼**——這是測試能夠在「用真的 provider」與「結果可斷言」之間同時成立的關鍵。詳見 I-07。

---

## 8. 由 provider 原始碼推導的隱含需求（I）

以下每一條都附上 `services/payment-provider/provider.py` 的事實依據。這些是題目沒有明寫、但 provider 的實際行為會強迫我們面對的需求。

| Id | 隱含需求 | provider 的事實 |
|----|---------|----------------|
| **I-01** | **去重必須以事件 id 為鍵，而不是付款 id** | 同一筆付款會產生兩個事件（`payment.succeeded`、`payment.refunded`），兩者的 `data.payment.id` 相同。以付款 id 去重會把退款吃掉 |
| **I-02** | **重複投遞是常態，不是例外** | 投遞成功後有 `DUPLICATE_RATE`（預設 **0.3**）的機率再送一次；重試序列是 `[0,1,2,4,8,16]` 秒共 **6 次**嘗試 |
| **I-03** | **2xx 必須代表「已經入帳」** | 回應 2xx 之外、或 5 秒無回應就會重送；回了 2xx 卻還沒寫入，事件就永遠不會再來 |
| **I-04** | **處理必須在 5 秒內完成** | `DELIVERY_TIMEOUT = 5`；超時算失敗 |
| **I-05** | **退款事件要能在入帳事件之前抵達** | 兩個事件各自開一條 thread、各自 `time.sleep(delay)` 後重試投遞，順序沒有任何保證；重試與重複會讓交錯更嚴重 |
| **I-06** | **退款事件的 `credits` 是正數，語意是沖回** | 退款送出的是 payment 快照，`credits` 仍等於 `amount_cents`，只有 `status` 變成 `refunded` |
| **I-07** | **測試要能決定性地觸發重複，而不是等機率** | `DUPLICATE_RATE` 是環境變數（可設 0 或 1），`POST /events/{id}/resend` 可以精準重送一次。兩者都不需要改 provider 的程式碼 |
| **I-08** | **簽章要對 raw bytes 驗，不能先反序列化再重組** | 簽的是 `"<t>." + json.dumps(event)` 的原始 bytes；重新序列化後鍵序與空白都可能不同 |
| **I-09** | **每次投遞的 `t` 是當下時間，不是事件時間** | `sign(payload, int(time.time()))` 在每次 `deliver_once` 時重算；`event.created` 與 `t` 是兩件事 |
| **I-10** | **事件可能永久送不到** | 6 次嘗試全失敗就 `gave up`；之後只有人工 `resend` 會再送。因此需要一條對帳路徑 |
| **I-11** | **provider 沒有「查某筆付款」的 API** | 只有 `GET /events`。對帳只能用事件清單與自己的紀錄比對 |
| **I-12** | **同一筆付款不會被退款兩次** | `status == "refunded"` 時回 `409`。但**退款事件本身仍可能被重複投遞**，所以我方的幂等性不能依賴這一點 |
| **I-13** | **provider 不認識 idempotency key** | `POST /payments` 沒有任何幂等機制；重複呼叫會產生兩筆不同付款（兩張不同的事件）。這是正確行為，不是缺陷 |
| **I-14** | **未知的事件型別要能安全忽略** | 目前只有兩種 `type`，但事件型別是會長出來的；把未知型別當失敗會讓 provider 永遠重送同一封 |

---

## 9. 題目未定義、必須自己決定的事（U）

這一節不給答案，只負責把「題目沒說」這件事記錄下來。每一條的決定與理由在 `plan-wallet-go-postgres.md` 第 2 節。

| Id | 未定義的事 | 兩種合理答案 |
|----|-----------|-------------|
| **U-01** | 查一個從來沒有付款過的 user | `404`，或 `200` + `balance: 0` |
| **U-02** | 退款金額大於現有餘額（credits 已經被花掉了） | 允許負餘額（真的欠），或拒絕退款（帳目說謊） |
| **U-03** | 扣款要不要幂等 | 題目沒有 idempotency key；但扣款是客戶端在不可靠網路上發起的寫入 |
| **U-04** | 餘額不足用哪個 4xx | `422`、`409`、`400` 都在 4xx 範圍內；題目只說 4xx |
| **U-05** | `amount` 的合法範圍 | 正整數？允許 0？浮點數？上界？ |
| **U-06** | wallet 的 API 要不要認證 | 題目沒有任何認證需求；webhook 端點有簽章，另外兩支沒有 |
| **U-07** | 要不要跨重啟保存 | 題目說「any storage you like」，等於把這題丟回來 |
| **U-08** | 多幣別、多租戶 | 題目只有 credits 與 `user_id` |
| **U-09** | webhook 的回應 body 要長什麼樣 | provider 只看狀態碼 |
| **U-10** | 退款事件抵達、但該付款從未入帳過 | 直接記負數（可交換），或先擱置等入帳事件 |

> 這十條的**決定與理由**在 [`plan-wallet-go-postgres.md`](./plan-wallet-go-postgres.md) 第 2 節；
> 其中哪幾條是「我替你決定的、你應該過目」，整理在同一份文件的 **16.3 需要你決策的事項**。

---

## 10. Bonus 空間（B）

題目明寫「最小需求之外、你設計並實作的任何能力都是加分」。以下是從 I 與 U 推導出來、真的有人會在 production 需要的東西，依「這題的風險」排序。

| Id | 能力 | 為什麼它屬於 production |
|----|------|----------------------|
| **B-01** | 事件 inbox（以事件 id 為主鍵）＋ exactly-once 入帳 | I-01、I-02 直接要求 |
| **B-02** | 簽章驗證：constant-time 比對、timestamp 容忍窗、raw bytes | I-08、I-09；沒有它，任何人都能幫自己加錢 |
| **B-03** | append-only 分類帳（ledger）＋ 餘額欄位，兩者同交易 | 「為什麼餘額是這個數字」只能由分類帳回答 |
| **B-04** | 扣款的 idempotency key | U-03 |
| **B-05** | 併發正確性：扣款不會超扣、同時投遞的四份只入帳一次 | 這是 R-05 在多執行緒下的真實形狀 |
| **B-06** | 對帳：比對 `GET /events` 與自己的 inbox，補回永久失敗的事件 | I-10、I-11 |
| **B-07** | 可觀測性：`/health`、request id、事件處理計數 | demo 時要能解釋發生了什麼 |
| **B-08** | 稽核查詢：列出某個 user 的分類帳 | 讓 B-03 看得見 |
| **B-09** | 錯誤碼穩定化（機器可讀的 `error` 代碼） | 讓 4xx 可被客戶端處理，而不是靠文字 |
| **B-10** | RealSpec 規格套件（`spec/` ＋ `realspec validate` ＋ BDD 場景） | 讓以上每一條都是**可執行的斷言**，而不是 PR 裡的形容詞 |

---

## 11. 時間配置（題目原文）

| 階段 | 時間 | 活動 |
|------|------|------|
| 理解需求與設計 | ~5 分 | 讀題、規劃、與面試官對齊 |
| 用 AI 實作 | ~25 分 | 在 `wallet/` 建出解法 |
| Demo 與檢查 | ~5 分 | curl demo；面試官對服務跑檢查 |
| Q&A | ~10 分 | 面試官提問 |

這份文件與 `plan-wallet-go-postgres.md` 是第一階段（~5 分）的產物。25 分鐘能放進去什麼、放不進去什麼，在 `plan-wallet-go-postgres.md` 第 10 節有逐分鐘的切分。

---

## 12. 需求追溯表

`spec/` 尚未產生；下表是**規劃中的歸屬**，用來保證每一條需求都有一個會去斷言它的地方。

| 需求 | spec 產物 | 計畫中的 feature 檔 |
|------|-----------|-------------------|
| R-01、R-02、I-03 | `openapi/openapi.yaml`、`spec.md` | `api/credit.feature` |
| R-03、U-01 | `openapi/openapi.yaml` | `api/wallets.feature` |
| R-04、U-04、U-05、B-09 | `openapi/openapi.yaml` | `api/spend.feature` |
| R-05、B-05 | `spec.md`（NFR 併發） | `api/spend.feature` |
| I-01、I-02、I-07、B-01 | `spec.md`（inbox） | `api/dedupe.feature` |
| B-02、I-08、I-09 | `spec.md`（簽章） | `api/signature.feature` |
| I-05、I-06、I-10、U-02、U-10 | `spec.md`（退款與順序） | `api/refund.feature`、`api/ordering.feature` |
| I-03、I-04、U-07 | `spec.md`（故障模型） | `api/resilience.feature` |
| I-14、B-07、B-08 | `openapi/openapi.yaml` | `api/observability.feature` |
| B-06、I-11 | `spec.md`（對帳） | `api/reconcile.feature` |
| R-21 | `plan-wallet-go-postgres.md` 第 11 節 | demo 腳本（與 feature 斷言同一組事實） |
| R-30、R-31 | — | 由 PR diff 保證 |

---

## 13. 非目標

- 不實作使用者註冊、登入、授權（U-06：題目沒有這個需求，而且 webhook 端點的身分是簽章給的）。
- 不實作購買流程的前端；credits 的入口只有 provider 的 webhook。
- 不實作多幣別、匯率、稅、發票。
- 不實作部分退款——provider 不支援（`POST /payments/{id}/refund` 沒有金額參數）。
- 不改 `services/`（R-30）。
