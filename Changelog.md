# Changelog

## v0.3.0

### 升级须知

- 面向普通用户的接口改用单独的 schema，去掉了上游成本、倍率与账号字段：`GET /usage/logs` 返回 `MyRequestLogPage`（无 `upstream_cost_micro`、`tier_multiplier`、`model_multiplier`、`retries`、`account_id`、`session_hash`、`client_ip`），`GET /usage/summary` 返回 `MyUsageSummary`（无 `upstream_cost_micro`），`/subscriptions`、购买、续费、自动续费返回 `MySubscription`（无 `account_id`、`user_id`、`current_period.used_upstream_micro`）。管理端接口不变。
- `GET /me/availability` 中 plan 的 `account_status` 只剩 `normal` / `stale` / `cooldown` / `unavailable`（原来的 `error` 改为 `unavailable`，停用的账号也是 `unavailable`），此时 `five_hour` 与 `period` 的可用量为 0。
- 新环境变量 `BRIDGE_TIMEZONE`（默认 `Asia/Shanghai`），用于管理概览按天切分；二进制内置时区库。
- 前端构建改用 pnpm，产物不再提交到仓库（`backend/internal/webui/dist` 只保留 `.gitkeep`，未构建时返回 `placeholder/index.html`）。Docker 镜像与 CI 先构建前端再构建 Go。

### 前端

- 完整的面板：React + Vite + TypeScript（strict）、TanStack Router（文件式路由）/ Query / Table / Virtual、react-hook-form + zod、Recharts、i18next（zh-CN 默认，en）。shadcn/ui 组件源码在仓库内并按「试剂标签」设计规范改造（零圆角、无阴影、墨 / 试剂黄 / 危险红，Archivo 压窄 + IBM Plex Mono，字体自托管）。深浅色跟随系统，可手动切换并记住。
- 用户侧：登录、概览（汇总条、每 30 秒刷新的可用额度、近 14 天按档位花费、Plan 订阅卡片、最近请求、我的 Key）、API Key（档位开关与拖动 / 键盘排序、绑定订阅、完整 key 展示与复制）、用量明细（筛选、按天 / 小时与按模型图表、游标分页 + 虚拟滚动、详情抽屉）、Plan 订阅（续费、自动续费、购买前显示购买后余额）、余额与账单、设置。
- 管理侧：概览（今日汇总、账号状态分布、待分配提醒、Stream 积压与对账、SSE 实时请求流）、用户（搜索、新建、详情：资料、余额调整、key 停用、订阅、近期用量、重置密码、停用）、上游账号（汇总条、筛选、卡片网格与 SSE 实时刷新、先校验后保存的导入、详情页的配置 / 运行时 / 图表 / 估算明细 / 绑定订阅，删除冲突时逐条列出并可去换绑）、Plan、订阅（待分配队列、分配时显示预留与容量和超卖预警、换绑、取消退款、直接开通）、请求日志、账本、审计日志、safeguard 事件、分组的系统设置（美元与人类可读时长输入）。
- 桌面端左侧导航 + ⌘K / Ctrl+K 命令面板；手机端用户侧底部四个标签、管理侧抽屉菜单；宽表格在手机上改为卡片。后端错误码映射为中英文文案；破坏性操作二次确认；开关类操作乐观更新并在失败时回滚。

### 后端

- 可用额度的重置次数统一为只计严格早于 now+T 的重置：n(T) = ⌈(now+T − reset) / 周期⌉（reset ≥ now+T 时为 0），5h 与 7d 相同；原实现在后续重置恰好落在 now+T 时多算一次。
- 新增 `GET /admin/overview`：今日（服务器时区）请求数与 billed / upstream 合计及按档位拆分、账号按状态计数（含降权中）与总空闲 / 总预留、待分配订阅数、Stream 积压、最近一次对账。
- 新增面板所需接口与字段：`POST /admin/accounts/validate`（导入前校验，不保存）、`GET /admin/users/{id}/keys`、`PUT /admin/keys/{id}/enabled`、管理端请求日志的 `status_code` 筛选；管理端订阅带 `username`、`account_name`；账号带 `sold_weekly_quota_micro`、`sold_upstream_micro`、`week_total_micro`、`k`（来自 cpa-quota），运行时带 `penalized`；SSE 事件数据定义为 `AccountStatusEvent`、`RequestSettledEvent`。
- `cmd/fakecpa`：可单独运行的多实例假 CPA（额度随调用增长、窗口到点重置、上报 unified 限额响应头、提供演示用倍率表）；`bridge seed-demo` 生成演示数据；`make dev` 一键启动本地环境。

## v0.2.0

### 升级须知

- 只服务 Claude：删除了 `/v1/responses` 和 `/v1/chat/completions`（现在返回 Anthropic 格式的 404），只保留 `/v1/messages`、`/v1/messages/count_tokens`、`GET /v1/models`。用这两个端点的客户端需要改用 Messages 协议。
- 新迁移 `00002_claude_only` 给账号、plan、订阅加了只允许 `claude` 的 CHECK 约束（已删除的账号与 plan、已过期或取消的订阅不受约束）。库里还有未删除的非 Claude 账号或 plan、或 active / pending 的非 Claude 订阅时迁移会报错停止：先用 v0.1.x 删除或取消它们再升级。
- 面板 API 的 `SubscriptionKind` 只剩 `claude`；新增错误码 `unsupported_subscription`、`account_has_subscriptions`、`cpa_quota_unavailable`。升级前已登录的面板会话不在新的会话集合里，下一次请求时自动补登记。

### 后端

- 只保留 Claude Messages 协议：删除 Responses / Chat Completions 的转发、usage 解析，以及 Chat 流式 `include_usage` 的插入与剥离。对客户端不做任何校验，任何客户端发到 `/v1/messages` 的请求都原样透传；会话标识提取保持完整。`GET /v1/models` 返回启用账号可用模型的并集。
- 上游账号只接受 Claude 订阅：导入、编辑、测试时 `/quota` 返回的 subscription 不是 claude 即拒绝（`unsupported_subscription`）；plan 只能是 claude；已导入的账号之后报告别的订阅时由轮询置为 error。去掉了账号类型与 plan 类型不一致的判定（约束保证一致）。
- 会话失效：Redis 为每个用户维护会话集合。用户自己改密码保留当前会话、删除其他会话；管理员重置密码、停用、修改角色或删除用户时删除该用户全部会话，多副本立即生效。
- 上游账号删除保护：绑定了 active / pending_assignment 订阅的账号禁止删除，返回 409 `account_has_subscriptions` 并列出这些订阅；停用不受影响。删除与订阅绑定在账号行上互斥。
- 管理员账号详情（只读读取 cpa-quota 的表）：`GET /admin/accounts/{id}/estimate`、`/k-trend`、`/quota-series`（超过 2000 点按时间桶降采样）、`/cost-compare`（cpa-quota 与本项目的上游成本按时间桶并排）。只读事务、3 秒语句超时；schema 或表不存在时 503 `cpa_quota_unavailable`。
- 用户可用额度 `GET /me/availability`：按账号的 5h / 周窗口容量（含未来重置、plan 预留）估算普通与强制调用在 1h / 5h / 24h / 7d 的可用量，换算为用户价格并与余额取小；列出每份 plan 的 5h 与本期可用量。不返回任何账号信息。账号无关部分由 leader 在每次额度刷新后写入 Redis 缓存。

## v0.1.0

### 升级须知

- 首个版本。需要 PostgreSQL（与 cpa-quota 共用同一个库，本项目的表在 `BRIDGE_SCHEMA`，默认 `bridge`）和单实例 Redis（必须 `maxmemory-policy noeviction` 并开启 AOF）。必填环境变量：`BRIDGE_DATABASE_URL`、`BRIDGE_REDIS_URL`、`BRIDGE_SECRET_KEY`。
- 前端本版本只有占位页；面板 API 已完整，规格见 `api/openapi.yaml`。

### 后端

- `/v1/messages`、`/v1/messages/count_tokens`、`/v1/responses`、`/v1/chat/completions` 原样转发给 CPA（仅在 Chat Completions 流式缺少 `include_usage` 时最小化插入，并把 usage chunk 从回给客户端的流中去掉）；流式逐块 flush，只设空闲超时，客户端断开即取消上游请求。
- 三个档位 plan / standard / force，按 key 配置的顺序逐档尝试，全部不放行时逐档列出拒绝原因；会话粘性（滑动 TTL，1h 缓存时延长）与按 5h 剩余比例加权、带水位线惩罚的随机选号；账号空闲额度 H_A = (week.remaining − S_A) − Σ 预留。
- 上游 429 / 401 / 403 / 5xx / 网络错误按规则冷却或置 error，并在同一档内换号重试（plan 档不换号）；`claude refusal` 透传并记录 safeguard 事件。
- 计价与 cpa-quota 完全一致（复制其 pricing 包）；每个请求记录 upstream_cost 与 billed（× 档位倍率 × 模型倍率），金额统一为 micro-USD 整数。
- Redis 实时钱包（余额与 plan 本期额度）：Lua 原子预扣与结算，PG 基准值按版本号同步，key 丢失可重建；结算记录写入 Redis Stream，由多副本消费者批量落库（request_logs 按月分区、小时汇总表、plan 用量、按用户汇总的 api_charge），重复消费只计一次；每日对账。
- plan 商品与订阅：余额购买（不允许透支）、管理员分配 / 换绑账号（超额只警告）、直接开通、取消并退款、手动续费与自动续费、到期处理；周期与账号 week 窗口对齐，提前 reset 开始新一期。
- 后台任务（Redis leader 锁）：拉取 /quota 与 /v1/models、toll 倍率表刷新（存 PG 与 Redis，失败沿用旧表）、续费与到期、预建分区、对账、Stream 修剪。
- 面板 API（`/api/v1`，oapi-codegen 生成 chi strict server）：登录（argon2id、Redis 会话、HttpOnly + SameSite=Lax Cookie、登录限频、写操作要求 `X-Requested-With`）、key、余额与 ledger、plan 与订阅、用量日志与汇总、模型列表；管理端的用户、上游账号（导入前校验 /quota 与 /v1/models、实时状态）、plan、订阅、settings、各类日志、倍率表、对账，以及跨副本的 SSE 事件。管理员写操作全部记入审计日志。
- `/healthz`、`/readyz`、Prometheus 指标（单独监听）；Dockerfile、docker-compose（postgres:18 + redis:8）、CI 与自动发版。
