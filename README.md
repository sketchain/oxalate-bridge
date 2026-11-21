# oxalate-bridge

自建的 Claude API 中转站：用户与 API key、token plan、余额、计费和路由全部由本项目管理，上游是多个 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）v8 实例。每个 CPA 实例只挂一个 Claude 订阅账号（Pro / Max 5x / Max 20x），额度估算来自 CPA 上的 [cpa-quota](https://github.com/sketchain/cpa-quota) 插件。中转站只服务 Claude Messages 协议，只接受 Claude 订阅的上游账号。

本仓库目前是第三轮（v0.3.0）：后端完整，前端面板（`web/`，React + TanStack）已完成，用户侧与管理侧都在同一个应用里。界面截图见 [docs/screenshots](docs/screenshots)。

## 仓库结构

```
api/openapi.yaml         面板 API 的唯一规格来源（/v1/* 转发接口不在其中）
backend/                 Go 后端（单二进制）
  cmd/bridge             入口
  internal/api           oapi-codegen 生成的 chi strict server + handler 实现
  internal/proxy         /v1/* 请求主链路
  internal/router        账号目录、逐档判定、选号、H_A / 预留 / 周期计算
  internal/avail         用户可用额度预估（账号容量 C5 / C7、普通与强制池子、plan 视角）
  internal/rstate        Redis 实时状态（钱包与预扣、并发租约、RPM、粘性、分钟桶、Stream）
  internal/settle        结算记录与 Stream 消费者（批量落库）
  internal/store         PG 事务、只增不改的 ledger、outbox
  internal/jobs          leader 选举与后台任务
  internal/pricing       从 cpa-quota 复制的计价包（口径完全一致）
  internal/cpaquota      只读查询 cpa-quota 的表（schema.sql 只是给 sqlc 用的表结构参考，不是迁移）
  internal/sessionid     会话标识提取（移植自 CPA ExtractSessionInfo）
  internal/db            goose 迁移（嵌入）、sqlc 查询与生成代码、分区维护
  internal/webui         go:embed 前端产物（dist/ 由 web/ 构建写入，仓库里只有 .gitkeep；未构建时返回 placeholder/index.html）
  internal/fakecpa       假 CPA（测试用，也被 cmd/fakecpa 用来本地开发）
  internal/demo          `bridge seed-demo` 演示数据
  internal/e2e           集成与端到端测试
  cmd/fakecpa            可单独运行的假 CPA（多个实例，会上报额度响应头、返回带 usage 的 SSE）
web/                     前端面板：React 19 + Vite + TypeScript（strict）+ TanStack Router/Query/Table/Virtual，
                         shadcn/ui（源码在 src/components/ui，已按「试剂标签」规范改造）+ Tailwind 4，
                         react-hook-form + zod，Recharts，i18next（zh-CN 默认、en）
  src/api                openapi-typescript 生成的类型（schema.d.ts）、openapi-fetch 客户端、查询键与 hooks
  e2e                    Playwright 冒烟测试与截图
scripts/e2e.sh           构建 → fakecpa → seed-demo → bridge → Playwright
docs/screenshots         用户侧与管理侧主要页面的浅色 / 深色 / 手机截图
```

## 工作方式

### 请求主链路（/v1/messages、/v1/messages/count_tokens、GET /v1/models）

只服务 Claude Messages 协议。`/v1` 下的其他路径（包括 v0.1 的 `/v1/responses`、`/v1/chat/completions`）返回 Anthropic 格式的 404（`not_found_error`）。对客户端不做任何校验：任何客户端发到 `/v1/messages` 的请求都接受并原样透传，客户端区分由 CPA 侧插件处理。`GET /v1/models` 返回所有启用且未处于 error 的账号可用模型的并集。

1. 鉴权：`Authorization: Bearer` 或 `X-Api-Key`。进程内 L1（10 秒）→ Redis L2（10 分钟）→ PG；key / 用户改动写 outbox，由投递器删除 L2 并 pub/sub 通知所有副本清 L1。
2. 请求体只读一次并原样保留；用 gjson 取 `model`、`stream`、`max_tokens`、`cache_control` 的 1h ttl。
3. 会话标识按 CPA `ExtractSessionInfo` 的优先级提取（移植，未直接 import：直接依赖会让二进制增加约 15 MB），取不到用 key id；只存哈希。
4. 用户与 key 两个维度的并发（Redis 租约，带过期，进程崩溃自动回收）和 RPM（60 秒滑动窗口），超限 429。
5. 按 key 的 `tiers` 顺序逐档尝试 plan → standard → force（顺序由 key 决定）。只有「这一档不放行」才切下一档，上游报错不切档；全部不放行时 429，`error.rejections` 逐档列出原因。
6. 预扣（Lua 原子操作）：估算 = 请求体字节 / 3 按输入价 + min(max_tokens, 4096) 按输出价，乘档位与模型倍率，再与 `hold_cap` 和可用额取小。可用额 > 0 才放行，最后一笔允许扣成负数。
7. 原样转发给 CPA，绝不改写请求体；响应（含 SSE）逐块原样回给客户端，同时读取 usage。
8. 还没向客户端写出任何字节前：429 → 冷却到 Retry-After / 已用尽窗口的 reset_at / 60 秒；401/403 → 账号置 error；5xx / 网络错误 → 冷却 30 秒；然后同档换号重试（最多 `max_retries` 次，plan 档不换号）。`claude refusal` 开头的 400 不重试、原样透传并记 safeguard 事件。
9. 结算（Lua 原子操作）：退回预扣、计入待落库额、累加账号分钟桶、续期粘性映射，并把完整结算记录 XADD 到 Redis Stream。

### 实时钱包（余额与 plan 本期额度）

Redis 中每个钱包（用户余额，或某订阅某一期的额度）由三部分构成：

- `base`：PG 已提交的值（余额，或本期 quota − used）及其**版本号**（`users.balance_version` / `plan_usage_periods.version`）；只会被更高版本覆盖。
- `pending`：Redis 已结算、Stream 里尚未落库的金额；每条 Stream 记录落库后按消息 id 只减一次。
- `holds`：在途请求的预扣（ZSET，带过期时间，读的时候先清掉过期的）。

可用额 = base − pending − holds。管理员充值、购买、退款等写操作先提交 PG，再由 outbox 投递「绝对值 + 版本」；消费者落库后也用同样方式同步。三者顺序任意都收敛到 PG − 未落库金额；key 丢失时按 PG 值重建即可。Redis 必须 `maxmemory-policy noeviction` 并开启 AOF。

### 落库

Stream 消费者（consumer group，多副本可同时消费）每批一个 PG 事务：插入 `request_logs`（按月分区，唯一键 `(created_at, request_id)`，重复投递冲突即跳过，只有新插入的记录参与后续汇总）、更新 `plan_usage_periods`、更新小时汇总表 `usage_hourly`、按用户把 standard/force 的扣费汇总成一条 `api_charge` 写入 ledger 并更新余额；提交后才 ack。每天（leader）对账：ledger 求和 = 余额、standard/force 扣费总和 = api_charge 总和。

### 档位与账号选择

- plan：订阅 active、在有效期内、模型允许、本期还有额度、绑定账号启用且未硬性用尽；固定走绑定账号，不看水位线。
- standard：余额 > 0；账号未停用、未 stale、未硬性用尽、H_A > `standard_min_headroom_micro`、账号并发未满。
- force：余额 > 0；账号未停用、未硬性用尽、并发未满（不看 H_A 和 stale）。
- H_A = (week.remaining_usd − S_A) − Σ 预留；S_A 是快照 observed_at 之后本账号的上游成本（Redis 分钟桶，保留 8 天）；预留 = 每个 active 订阅本期 max(0, quota − used) / plan 档位倍率。窗口过了 reset_at 就本地视为已重置。
- 选号：先看会话粘性（`aff:<会话哈希>`，滑动 TTL，默认 5 分钟，请求带 1h cache ttl 时 1 小时），否则按权重随机：5h 剩余比例，达到水位线再乘 `watermark_penalty`；快照缺少 5h 窗口数据时兜底用 week 剩余比例。
- plan 周期与绑定账号的 week 窗口对齐：当期 = [week.reset_at − 7 天, week.reset_at)，每个碰到的窗口都给满额；账号提前 reset 即开始新一期（reset_at 抖动在 10 分钟内视为同一期）。

### 计价

与 cpa-quota 的 `internal/pricing` 完全一致（直接复制该包）：输入 = model_ratio × 2（USD/百万 token），输出 = 输入 × completion_ratio，缓存读 = 输入 × cache_ratio，缓存写 = 输入 × create_cache_ratio，缺失比率按 1；模型名忽略大小写、`(high)` 选项、`/` 前的命名空间和 `-high`/`-low` 后缀；表里没有的模型成本为 0、标记 unpriced。token 按 Claude 口径拆分（输入、缓存读写分列，输出含推理）。

每个请求记 `upstream_cost`（按 toll 计算）和 `billed` = upstream_cost × 档位倍率 × 模型倍率。金额一律为 int64 micro-USD。

### 上游账号

- 只接受 Claude 订阅：导入、编辑、测试时 `/quota` 返回的 `subscription` 不是 `claude` 就拒绝（`unsupported_subscription`）；plan 的 `subscription` 也只能是 `claude`。数据库对未删除的账号与 plan、`active` / `pending_assignment` 的订阅加了 CHECK 约束。已导入的账号之后若 `/quota` 报告了别的订阅，轮询会把它置为 error（不改库里的订阅字段）。
- 删除保护：账号上还绑定着 `active` 或 `pending_assignment` 的订阅时不能删除，返回 409 `account_has_subscriptions`，`error.subscriptions` 列出这些订阅（id、用户、状态、plan 名）；先换绑或取消。停用不受影响。删除与绑定订阅在同一账号行上加锁互斥，不会漏掉并发绑定。

### 面板会话

会话存在 Redis（`sess:<token>`，滑动过期），每个用户另有一个会话集合（`usess:<user>`）：登录时加入、登出时移除，过期的 token 在下次登录时清理。

- 用户自己改密码：保留当前会话，删除其他所有会话。
- 管理员重置密码、停用、修改角色（以及删除用户）：删除该用户的全部会话（管理员给自己重置密码时保留当前会话）。
- 失效是对共享 Redis 的直接删除，多副本立即生效；中间件每次请求还会重新读取用户状态。

### cpa-quota 数据（管理员只读）

账号表里的 `instance`、`auth_index`（来自 `/quota`）定位 `BRIDGE_CPA_QUOTA_SCHEMA` 中 cpa-quota 的数据（表结构以 cpa-quota schema version 1 为准）：

- `GET /api/v1/admin/accounts/{id}/estimate`：`estimates` 表对应行，`detail` 原样返回。
- `GET /api/v1/admin/accounts/{id}/k-trend?days=N`：`k_trend_daily`（默认 30 天，最大 365）。
- `GET /api/v1/admin/accounts/{id}/quota-series?window=5h|7d&from=&to=`：`quota_report_windows` 中 `limit_key='unified'` 的 `used_percent` 时间序列（含 `reset_at`），超过 2000 点时切成 2000 个等宽时间桶、每桶取最后一个值。
- `GET /api/v1/admin/accounts/{id}/cost-compare?from=&to=&bucket=hour|day`：同一 UTC 时间桶内 cpa-quota `requests.cost_usd`（只算 `accounting='complete'`，按请求开始时间）与本项目 `usage_hourly.upstream_cost_micro` 并排，用来核对两边成本。

查询都在只读事务里执行（`search_path` 指向 cpa-quota 的 schema，语句超时 3 秒）；schema 或表不存在、或超时时返回 503 `cpa_quota_unavailable`。cpa-quota 的表不在本项目的迁移里。

### 用户可用额度（`GET /api/v1/me/availability`）

返回调用者本人视角的预估值（`estimated: true`、`computed_at`），不含任何账号名、数量或 id。每个账号（上游成本口径，micro-USD，沿用本地窗口重置）：

- T5 = safety_factor × five_hour.total_usd，T7 = safety_factor × week.total_usd；total 为 0（未知）时该维度容量为 0。
- r5 = max(0, five_hour.remaining − S5)，r7 = max(0, week.remaining − S7)；S 是该窗口快照（或本地重置）之后本账号的花费。paused、cooldown 时两个 r 都取 0，某窗口 used ≥ 100 时该窗口的 r 取 0，未来的重置照常计入。
- n(T) = 0（下次重置 ≥ now+T），否则 1 + ⌊(now+T − 下次重置) / 周期⌋；假设 5h 窗口重置后立即开始下一个，结果是上限。
- C5(T) = r5 + n5(T)·T5（没有 5h 窗口时为无穷大），C7(T) = r7 + n7(T)·T7（没有 week 窗口时为 0）。
- 强制调用（启用、非 error 的账号，stale 用最后一次快照）：F = min(C5, C7)；普通调用（启用、非 stale / cooldown / error）：S = max(0, min(C5, C7 − P·(1 + n7)))，P 为该账号的 plan 预留。
- 1h、5h、24h、7d 各自求和，乘档位倍率换成用户价格，再与用户可用余额取小（余额更小时 `limited_by_balance: true`），同时返回池子换算值和余额。
- 每份 active 订阅：本期起止、额度、已用、剩余（billed），绑定账号的状态（normal / stale / cooldown / error），`five_hour` = min(本期剩余, r5 × plan 倍率)，`period` = min(本期剩余, min(C5, C7)(到本期结束，若订阅更早到期则到到期时间) × plan 倍率)。`pending_assignment` 只返回状态。

和用户无关的部分由 leader 在每次拉取 /quota 之后重算并写入 Redis（10 分钟过期）；接口只读缓存再结合调用者的余额、倍率和订阅现算，缓存缺失时现算一次并写回。

## 配置

### 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `BRIDGE_LISTEN` | `:8080` | 对外监听地址（/v1、/api/v1、面板） |
| `BRIDGE_METRICS_LISTEN` | `127.0.0.1:9090` | Prometheus `/metrics` 单独监听，默认不对外 |
| `BRIDGE_DATABASE_URL` | 必填 | PostgreSQL 连接串（与 cpa-quota 共用同一个库） |
| `BRIDGE_SCHEMA` | `bridge` | 本项目的表所在 schema，启动时自动创建并迁移 |
| `BRIDGE_CPA_QUOTA_SCHEMA` | `cpa_quota` | cpa-quota 插件的 schema（管理员账号详情接口只读访问） |
| `BRIDGE_REDIS_URL` | 必填 | Redis 连接串（单实例，不支持 Cluster） |
| `BRIDGE_SECRET_KEY` | 必填 | 32 字节 base64，用于 AES-GCM 加密密钥字段与签名（`openssl rand -base64 32`） |
| `BRIDGE_TRUSTED_PROXIES` | 空 | 可信反代（IP / CIDR，逗号分隔），只信任它们的 `X-Forwarded-For` |
| `BRIDGE_BOOTSTRAP_ADMIN_USERNAME` / `BRIDGE_BOOTSTRAP_ADMIN_PASSWORD` | 空 | 库里没有管理员时首次启动创建 |
| `BRIDGE_COOKIE_SECURE` | `true` | 会话 Cookie 是否带 Secure（纯 HTTP 本地调试时设为 `false`） |
| `BRIDGE_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

### 运行时参数（settings 表，`PATCH /api/v1/admin/settings` 修改，所有副本即时生效）

| 键 | 默认值 | 说明 |
| --- | --- | --- |
| `default_user_concurrency` | 3 | 新建用户的并发上限 |
| `watermark_5h_percent` | 70 | 5h 水位线（账号可单独覆盖） |
| `watermark_penalty` | 0.1 | 超过水位线后的权重系数 |
| `standard_min_headroom_micro` | 500000 | standard 档要求的最小 H_A（0.5 USD） |
| `sticky_ttl_seconds` / `sticky_ttl_1h_seconds` | 300 / 3600 | 会话粘性 TTL；请求带 1h cache ttl 时用后者 |
| `tier_multipliers` | `{plan:1, standard:1, force:1}` | 档位倍率 |
| `model_multipliers` | `{}` | 模型倍率（规范化后的模型名 → 倍率，默认 1） |
| `hold_cap_micro` | 1000000 | 单次预扣上限（1 USD） |
| `hold_default_max_tokens` | 4096 | 估算输出 token 的上限 / 缺省值 |
| `max_retries` | 2 | 同档换号重试次数 |
| `quota_poll_seconds` | 30 | 拉取 /quota 的间隔 |
| `stale_after_seconds` | 180 | 连续失败超过它标记 stale |
| `models_poll_minutes` | 10 | 拉取 /v1/models 的间隔 |
| `price_url` / `price_refresh_hours` | toll / 24 | 倍率表地址与刷新间隔 |
| `upstream_idle_timeout_seconds` | 300 | 上游空闲超时（无总超时） |
| `auto_renew_lead_hours` | 24 | 自动续费提前量 |
| `cooldown_5xx_seconds` / `cooldown_429_default_seconds` | 30 / 60 | 冷却时长 |

## 部署

```bash
cp .env.example .env   # 填 BRIDGE_SECRET_KEY、POSTGRES_PASSWORD、管理员账号
docker compose up -d   # bridge + postgres:18 + redis:8（appendonly、noeviction）
```

镜像：`ghcr.io/sketchain/oxalate-bridge`（linux/amd64、linux/arm64）。服务无状态，可多副本：共享状态只在 PG 与 Redis，后台任务通过 Redis leader 锁只在一个副本执行。

健康检查：`/healthz`（存活）、`/readyz`（PG + Redis）。指标见 `/metrics`：各档请求数、拒绝原因、上游错误、重试、首字延迟、账号 H_A、Stream 积压、对账差异。

## 开发

需要 Go 1.27、Node 22 + pnpm 10（`corepack enable`）、Docker（`make dev` 用来起 PostgreSQL 与 Redis）。

### 一键本地开发

```bash
make dev        # compose 起 PG(55432) 与 Redis(56379)，运行 fakecpa、seed-demo（只在首次）、后端、Vite
```

打开 <http://localhost:5173>：管理员 `admin / admin-pass-123`，普通用户 `alice`、`bob`、`carol`（密码 `demo-pass-123`）。
Vite 把 `/api` 和 `/v1` 代理到 `127.0.0.1:8080` 的后端，前端改动热更新；改了 Go 代码重新 `make dev`。
`make dev-down` 停容器，`make dev-reset` 清空数据（下次重新生成演示数据）。

演示数据（`bridge seed-demo -fakecpa http://127.0.0.1:18400`）：4 个指向 fakecpa 的账号（正常、降权中、5h 已满、几分钟后变成数据过期）、
3 个 plan、alice 的一份有效订阅和一份待分配订阅、bob 的管理员开通订阅、若干 key、真实经过中转的请求和 13 天历史用量、一条 safeguard 事件；
库里没有 cpa-quota schema 时还会建一个演示用的（估算、k 趋势、5h / 7d 曲线、成本对比）。已有数据时不做任何事。

### 其他命令

```bash
make generate   # sqlc + oapi-codegen + openapi-typescript（版本与 CI 一致）
make web        # 前端构建 → backend/internal/webui/dist（不提交）
make lint lint-web test test-web
make build && BRIDGE_DATABASE_URL=... BRIDGE_REDIS_URL=... BRIDGE_SECRET_KEY=... ./backend/bridge
# Playwright：需要空的 PG 库与 Redis 库
BRIDGE_DATABASE_URL=... BRIDGE_REDIS_URL=... make e2e          # 冒烟测试
BRIDGE_DATABASE_URL=... BRIDGE_REDIS_URL=... make screenshots  # 重新生成 docs/screenshots
```

不构建前端也能编译 Go：此时面板地址返回占位页。

测试：

```bash
cd backend
BRIDGE_TEST_DATABASE_URL='postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable' \
BRIDGE_TEST_REDIS_URL='redis://127.0.0.1:6379/0' \
go test -race -count=1 ./...
```

集成与端到端测试读取这两个变量，每个测试使用独立 schema（及对应的 Redis key 前缀）并在结束时清理；未设置时跳过。端到端测试用 Go 写的假 CPA（`internal/fakecpa`，一个 Claude 订阅账号）提供 /quota、/v1/models 以及带 usage 的 Claude Messages（流式与非流式）、count_tokens、refusal、429、5xx。cpa-quota 接口的测试在临时 schema 里按 `internal/cpaquota/schema.sql` 建表。

修改 `api/openapi.yaml` 或 `backend/internal/db/queries` 后必须重新生成并提交（含 `web/src/api/schema.d.ts`），CI 会检查生成结果与提交是否一致；新增页面后 `pnpm build` 会更新 `web/src/routeTree.gen.ts`，同样要提交。

前端 CI：typecheck、ESLint、Vitest（格式化、可用额度展示、档位链编辑、错误码映射）、zh-CN / en 语言文件 key 一致性检查，以及对着 fakecpa + 演示数据跑的 Playwright 冒烟测试。

## 发版

- `Changelog.md` 顶部的 `## vX.Y.Z` 与 `backend/internal/version/version.go` 中的 `Version` 一致时，main 上 Check 通过后 Auto release 打 tag 并触发 Release。
- Release 构建 linux/amd64 + linux/arm64 镜像推到 `ghcr.io/sketchain/oxalate-bridge`（`X.Y.Z`、`X.Y`、`latest`），发布说明取自 `Changelog.md`。

## 已知限制

- Redis 只支持单实例（Lua 脚本会访问以参数传入的 key，Cluster 下不成立）。
- 可用额度是预估：5h 窗口假设重置后立即开始下一个（上限），池子和账号容量是 leader 上次刷新时的值。
- cost-compare 两边按请求开始时间分桶；CPA 实例上不经过中转站的请求只出现在 cpa-quota 一侧。

## 许可

MIT，见 [LICENSE](LICENSE)。`backend/internal/pricing` 复制自 sketchain/cpa-quota（MIT，同一作者）；`backend/internal/sessionid` 按 CLIProxyAPI（MIT）的会话提取逻辑移植。
