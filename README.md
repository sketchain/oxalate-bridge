# oxalate-bridge

自建的 AI API 中转站：用户与 API key、token plan、余额、计费和路由全部由本项目管理，上游是多个 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）v8 实例。每个 CPA 实例只挂一个订阅账号（Claude Pro / Max 5x / Max 20x 或 ChatGPT Team），额度估算来自 CPA 上的 [cpa-quota](https://github.com/sketchain/cpa-quota) 插件。

本仓库目前是第一轮：后端完整，前端只有占位页（面板 API 与 OpenAPI 规格已经齐全）。

## 仓库结构

```
api/openapi.yaml         面板 API 的唯一规格来源（/v1/* 转发接口不在其中）
backend/                 Go 后端（单二进制）
  cmd/bridge             入口
  internal/api           oapi-codegen 生成的 chi strict server + handler 实现
  internal/proxy         /v1/* 请求主链路
  internal/router        账号目录、逐档判定、选号、H_A / 预留 / 周期计算
  internal/rstate        Redis 实时状态（钱包与预扣、并发租约、RPM、粘性、分钟桶、Stream）
  internal/settle        结算记录与 Stream 消费者（批量落库）
  internal/store         PG 事务、只增不改的 ledger、outbox
  internal/jobs          leader 选举与后台任务
  internal/pricing       从 cpa-quota 复制的计价包（口径完全一致）
  internal/sessionid     会话标识提取（移植自 CPA ExtractSessionInfo）
  internal/db            goose 迁移（嵌入）、sqlc 查询与生成代码、分区维护
  internal/webui         go:embed 前端产物（dist/ 由 web/ 构建写入）
  internal/fakecpa       测试用假 CPA
  internal/e2e           集成与端到端测试
web/                     前端（本轮为占位页；构建产物写到 backend/internal/webui/dist）
```

## 工作方式

### 请求主链路（/v1/messages、/v1/messages/count_tokens、/v1/responses、/v1/chat/completions、GET /v1/models）

1. 鉴权：`Authorization: Bearer` 或 `X-Api-Key`。进程内 L1（10 秒）→ Redis L2（10 分钟）→ PG；key / 用户改动写 outbox，由投递器删除 L2 并 pub/sub 通知所有副本清 L1。
2. 请求体只读一次并原样保留；用 gjson 取 `model`、`stream`、`max_tokens`、`cache_control` 的 1h ttl。
3. 会话标识按 CPA `ExtractSessionInfo` 的优先级提取（移植，未直接 import：直接依赖会让二进制增加约 15 MB），取不到用 key id；只存哈希。
4. 用户与 key 两个维度的并发（Redis 租约，带过期，进程崩溃自动回收）和 RPM（60 秒滑动窗口），超限 429。
5. 按 key 的 `tiers` 顺序逐档尝试 plan → standard → force（顺序由 key 决定）。只有「这一档不放行」才切下一档，上游报错不切档；全部不放行时 429，`error.rejections` 逐档列出原因。
6. 预扣（Lua 原子操作）：估算 = 请求体字节 / 3 按输入价 + min(max_tokens, 4096) 按输出价，乘档位与模型倍率，再与 `hold_cap` 和可用额取小。可用额 > 0 才放行，最后一笔允许扣成负数。
7. 原样转发给 CPA（`/v1/messages`、`/v1/responses` 绝不改写请求体）。唯一例外：`/v1/chat/completions` 流式且未带 `stream_options.include_usage` 时插入它，并把 usage chunk 从回给客户端的流中去掉。
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
- 选号：先看会话粘性（`aff:<会话哈希>`，滑动 TTL，默认 5 分钟，请求带 1h cache ttl 时 1 小时），否则按权重随机：5h 剩余比例，达到水位线再乘 `watermark_penalty`；没有 5h 窗口的账号用 week 剩余比例。
- plan 周期与绑定账号的 week 窗口对齐：当期 = [week.reset_at − 7 天, week.reset_at)，每个碰到的窗口都给满额；账号提前 reset 即开始新一期（reset_at 抖动在 10 分钟内视为同一期）。

### 计价

与 cpa-quota 的 `internal/pricing` 完全一致（直接复制该包）：输入 = model_ratio × 2（USD/百万 token），输出 = 输入 × completion_ratio，缓存读 = 输入 × cache_ratio，缓存写 = 输入 × create_cache_ratio，缺失比率按 1；模型名忽略大小写、`(high)` 选项、`/` 前的命名空间和 `-high`/`-low` 后缀；表里没有的模型成本为 0、标记 unpriced。token 拆分：Claude Messages 用 Claude 口径（输入、缓存读写分列，输出含推理），Responses / Chat Completions 用 Codex 口径（缓存含在输入里，推理含在输出里）。

每个请求记 `upstream_cost`（按 toll 计算）和 `billed` = upstream_cost × 档位倍率 × 模型倍率。金额一律为 int64 micro-USD。

## 配置

### 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `BRIDGE_LISTEN` | `:8080` | 对外监听地址（/v1、/api/v1、面板） |
| `BRIDGE_METRICS_LISTEN` | `127.0.0.1:9090` | Prometheus `/metrics` 单独监听，默认不对外 |
| `BRIDGE_DATABASE_URL` | 必填 | PostgreSQL 连接串（与 cpa-quota 共用同一个库） |
| `BRIDGE_SCHEMA` | `bridge` | 本项目的表所在 schema，启动时自动创建并迁移 |
| `BRIDGE_CPA_QUOTA_SCHEMA` | `cpa_quota` | cpa-quota 插件的 schema（本轮只做存在性检查，面板后续只读） |
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

需要 Go 1.27、Node 22（仅前端构建），以及测试用的 PostgreSQL 与 Redis。

```bash
make generate   # sqlc + oapi-codegen（版本与 CI 一致）
make web        # 前端构建 → backend/internal/webui/dist
make lint
make build && BRIDGE_DATABASE_URL=... BRIDGE_REDIS_URL=... BRIDGE_SECRET_KEY=... ./backend/bridge
```

测试：

```bash
cd backend
BRIDGE_TEST_DATABASE_URL='postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable' \
BRIDGE_TEST_REDIS_URL='redis://127.0.0.1:6379/0' \
go test -race -count=1 ./...
```

集成与端到端测试读取这两个变量，每个测试使用独立 schema（及对应的 Redis key 前缀）并在结束时清理；未设置时跳过。端到端测试用 Go 写的假 CPA（`internal/fakecpa`）提供 /quota、/v1/models 以及带 usage 的三种协议、refusal、429、5xx。

修改 `api/openapi.yaml` 或 `backend/internal/db/queries` 后必须重新生成并提交，CI 会检查生成结果与提交是否一致。

## 发版

- `Changelog.md` 顶部的 `## vX.Y.Z` 与 `backend/internal/version/version.go` 中的 `Version` 一致时，main 上 Check 通过后 Auto release 打 tag 并触发 Release。
- Release 构建 linux/amd64 + linux/arm64 镜像推到 `ghcr.io/sketchain/oxalate-bridge`（`X.Y.Z`、`X.Y`、`latest`），发布说明取自 `Changelog.md`。

## 已知限制

- Redis 只支持单实例（Lua 脚本会访问以参数传入的 key，Cluster 下不成立）。
- Claude 账号经 `/v1/responses` 调用时，CPA 的协议转换把缓存写合并进了输入数，中转站看不到缓存写，这部分按输入价计费（cpa-quota 看到的是 Claude 原生口径，两边在这一点上会有差异）。Chat Completions 和 Messages 不受影响。

## 许可

MIT，见 [LICENSE](LICENSE)。`backend/internal/pricing` 复制自 sketchain/cpa-quota（MIT，同一作者）；`backend/internal/sessionid` 按 CLIProxyAPI（MIT）的会话提取逻辑移植。
