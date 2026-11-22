# Changelog

## v0.4.1

### 新增错误码

- 转发接口：`key_balance_quota_exhausted`。`quota_scope = balance`（仅余额）的 key 总额度或某个窗口用完时，只把 standard 和 force 两档从这次请求的档位链里去掉，这两档的拒绝原因就是这个码（窗口导致时附 `window` 与 `recovers_at`）；plan 档照常尝试。所有档都不放行时仍按原规则返回 429（rate_limit_error，请求日志 `error_type = tier_rejected`），`error.rejections` 逐档列出原因。中英文文案已补齐。
- `quota_scope = all` 不变：总额度或任何窗口用完，整个 key 拒绝（`key_quota_exhausted` / `key_window_exhausted`），不往下一档切。

### 新增接口字段

- `KeyLimitStatus.exhausted`（`GET /keys/{id}/limits`、`GET /admin/keys/{id}/limits`、`GET /public/key-usage`、`GET /v1/bridge/status`）：额度或窗口用完时给出 `code`、`window`、`recovers_at`、`plan_available`（仅余额口径为 true）和 `message`；总额度优先于窗口。
- `Settings` / `SettingsPatch` 新增 `public_query_ip_per_minute`（默认 30）、`public_query_key_per_minute`（默认 30）、`status_key_per_minute`（默认 60），取值 1–10000。

### 仅余额口径不再拦 Plan 档

- key 级别的额度检查和预扣按口径分流：仅余额口径额度用完时只标记 standard / force 不放行，Plan 档继续走原来的判定。
- 预扣：仅余额口径下 plan 档请求不受 key 额度和窗口约束；standard / force 的预扣额仍以 key 剩余额度和窗口剩余额度为上限（同一个 Lua 脚本，行为未变）。
- 查询页、key 详情抽屉显示用完提示：仅余额口径为黄色横条「普通 / 强制档不再放行，Plan 仍可用」，全部计入口径为红色横条「所有请求都会被拒绝」，窗口导致时带预计恢复时间。README 里的状态栏脚本改为读取 `limits.exhausted`，仅余额口径显示「Key 余额额度用完（Plan 仍可用）」。

### 公开接口限频可在运行时调整

- 凭 key 查询（按 IP、按 key）和状态栏接口（按 key）的限频从常量移进运行时设置，改动经 pub/sub 在所有副本即时生效；状态栏接口的 429 文案带上当前限额。
- 管理端「系统设置」新增「公开接口限频」分组，三个值做 1–10000 的整数校验（前后端都校验）。

### 测试

- 后端：两种口径下总额度用完、窗口用完后的行为；仅余额口径档位链切换（standard → plan）、只剩余额档时 429 逐档列出新原因码、Plan 也用完时同时列出 `plan_quota_exhausted` 与新原因码；预扣上限（仅余额口径 plan 请求在途时 key 上无预扣、standard 请求被窗口剩余额度封顶，全部计入口径 plan 请求被剩余额度封顶）；`exhausted` 字段；限频设置的范围校验、另一个副本即时生效、调小后立即限流、调大后当分钟立即放行。
- Playwright 冒烟：仅余额口径额度为 0 的 key 在查询页显示「Plan 仍可用」；管理员在「公开接口限频」里修改并校验范围。

## v0.4.0

### 升级须知

- 新迁移 `00003_key_limits_redeem`：`api_keys` 增加 `quota_limit_micro`、`quota_scope`（all / balance，默认 all）、`limit_5h_micro` / `limit_1d_micro` / `limit_7d_micro`、`expires_at`、`model_allowlist`、`ip_allowlist`、`allow_owner_query`，以及计量用的 `used_all_micro`、`used_balance_micro`、`usage_version`、`usage_reset_at`（老 key 的已用量从 `usage_hourly` 回填）。新表 `redeem_batches`、`redeem_codes`、`announcements`、`announcement_dismissals`；`ledger_entries.type` 增加 `redeem`；新增索引 `usage_hourly (key_id, hour)`、`request_logs (key_id, created_at DESC, request_id DESC)`。
- `ApiKey` 新增 `status`（active / disabled / expired）、上面的限制字段、`used_all_micro`、`used_balance_micro`、`usage_reset_at`；`ApiKeyCreate` / `ApiKeyUpdate` 可设置限制字段（空 = 不限，`null` 清除）。
- `AccountRuntime` 新增 `assignable`，`OverviewAccounts`（`/admin/overview` 与 `/admin/accounts` 的 `summary`）新增 `assignable` 计数：「可分配」改由后端统一判定。
- 账号的 `k` 优先取 `/quota` 的 `accounts[0].k`，没有时才取 cpa-quota 估算表的 `k`。`cpa.Account` 读取新的 `k` 字段，fakecpa 也会返回它。
- Redis 结算与落库脚本多了 key 计量的参数，新旧版本副本不要混跑（滚动升级时先停旧副本再起新副本）。

### 新增错误码

- 转发接口（Anthropic 原生错误格式，`error.code` 同时写入请求日志的 `error_type`，key 级别拒绝不会切到下一档）：`key_expired`（403，permission_error）、`ip_not_allowed`（403，permission_error）、`model_not_allowed`（403，permission_error）、`key_quota_exhausted`（429，rate_limit_error）、`key_window_exhausted`（429，rate_limit_error，附 `window` 与 `recovers_at`）。
- 面板接口：`redeem_code_invalid`、`redeem_code_used`、`redeem_code_void`、`redeem_code_expired`（400）、`redeem_locked`（429）。所有新错误码都有中英文文案。

### 新增接口

- `GET /v1/bridge/status`（转发域名）：鉴权同 `/v1/messages`，不计费、不写日志，每个 key 60 次/分钟。
- `GET /api/v1/public/key-usage`：免登录凭 key 查询（key 只认请求头），按 IP、按 key 各 30 次/分钟。
- `GET /api/v1/keys/{id}/limits`、`POST /api/v1/keys/{id}/reset-usage`、`GET /api/v1/keys/{id}/usage/daily`、`GET /api/v1/usage/keys`。
- `POST /api/v1/balance/redeem`、`GET /api/v1/announcements`、`POST /api/v1/announcements/{id}/dismiss`、`GET /api/v1/pricing`。
- 管理端：`PATCH /admin/keys/{id}`、`GET /admin/keys/{id}/limits`、`POST /admin/keys/{id}/reset-usage`、`GET|POST /admin/redeem-batches`、`GET /admin/redeem-batches/{id}/export`（CSV）、`GET /admin/redeem-codes`、`POST /admin/redeem-codes/{id}/void`、`GET|POST /admin/announcements`、`PATCH|DELETE /admin/announcements/{id}`。

### API key 限制

- 总额度、额度口径（全部计入 / 仅余额）、5h / 1d / 7d 滑动窗口、过期时间、模型白名单（`/v1/models` 同步过滤）、IP 白名单（CIDR，按信任代理解析的真实 IP）、允许凭 key 查询账户；用户和管理员都能设置，管理员修改写审计日志。
- 计量沿用钱包的「PG 基准 + 版本号 + pending」：Redis 维护两套累计用量（all / balance）和按分钟的窗口桶，结算脚本原子累加，消费者落库时锁住 key 行并只计入重置之后开始的请求；Redis 丢失时基准从 PG、窗口从最近 7 天的 `request_logs` 重建。
- 预扣同时受钱包、key 剩余额度和各窗口剩余额度约束（一个 Lua 脚本内完成），剩余 > 0 才放行，允许最后一笔超出。
- 重置已用额度：总额度从 0 重新计起，窗口不受影响。

### 前端

- Key 新建 / 编辑对话框分组（基本信息、档位与订阅、并发与频率、额度限制、访问控制）；额度口径用方形拨动开关（2px 墨框、无圆角、选中格墨底反色，radiogroup + 方向键 / Home / End 操作）。
- Key 列表新增状态与总额度列；点开 key 显示详情抽屉：各项限制、实时用量进度（窗口用完时显示预计恢复时间）、重置按钮、7 / 30 / 90 天每日用量图。
- 新的公开页面 `/query`：密码框输入 key（可切换显示），key 只在内存里；手机端可用；登录页有入口。
- 用量页新增「按 Key 对比」视图（前 10 个 key 的每日扣费折线 + 各 key 合计表）。
- 「余额与账单」页可以兑换兑换码；管理端新增「兑换码」页（生成批次、按批次与状态筛选、作废、导出 CSV）和「公告」页；面板顶部显示生效中的公告横幅（警告整条填黄，普通用墨框，可关闭，关闭状态记在服务端；正文只渲染换行与链接）。
- 新页面「模型与价格」（用户侧与管理侧导航都有）：搜索、三档切换，桌面表格、手机卡片，未定价模型单独标注。
- 管理端用户详情的 key 列表可以编辑限额、重置已用额度。
- 概览「近 14 天花费」一行改为顶部对齐，图表固定 280px，不再被 Plan 卡片拉高。
- 账号页汇总、筛选和管理概览的「可分配」使用后端的 `assignable`。

### 修正

- **k 值异常的根因是演示种子配错了**：`fakecpa.DemoProfiles` 里手写了 `K: 4.12 / 1.03 / 0.98 / 0.41`，注释把 k 误当成「5h 窗口每个百分点多少美元」，`bridge seed-demo` 把它原样写进演示 cpa-quota 的 `estimates.k`，桥读取 `estimates.k` 的路径本身没错。现在 k 一律按「周总额 ÷ 5h 总额」由 profile 的总额算出（演示四个账号为 8.25 / 7.6 / 9.2 / 6），fakecpa 的 `/quota` 也返回 `accounts[0].k`，种子与面板都以它为准；测试保证账号显示的 k 等于 `/quota` 的 `accounts[0].k`（没有时取估算表的 `k`），并且演示账号的 k 在 5–10 之间。
- 截图脚本每个用户只登录一次，不再触发登录限频（每 IP 15 分钟 30 次）。

### 测试与演示数据

- 后端：key 额度口径切换、滑动窗口边界与恢复时间、重置已用、各拒绝错误码与日志、预扣受 key 额度和窗口约束、Redis 丢失后重建；查询接口（开关为 false 时没有 `account`、限频、URL 里的 key 不被接受）；状态栏接口不计费不写日志；兑换码并发只成功一次、过期 / 作废不能兑换、连续失败锁定；公告生效时间段与按用户关闭；价格与「可分配」计数。
- Playwright 冒烟：创建带限额的 key 并在抽屉和查询页看到它、兑换码兑换后余额增加、公告横幅显示后能关闭、价格页能打开、14 天图表固定高度；手机端无横向溢出的页面加上了价格页、按 Key 对比和查询页。
- 截图新增查询页、key 详情抽屉、兑换码管理页、价格页（浅色、深色、手机），其余受影响的截图全部重新生成（概览带公告横幅）。
- `seed-demo` 新增带限额的 key（含开启凭 key 查询的「Claude Code」）、一批 20 个兑换码（一个已兑换、一个已作废）和一条公告。

## v0.3.1

### 升级须知

- `AccountRuntime` 新增 `display_status`（error > disabled > cooldown > five_hour_full / week_full / paused > stale > penalized > normal 的唯一显示状态）、`status_reason_code`（机器可读原因码，可能出现未知值）、`hard_reason`、`oversold`。`status_reason` 改为原始细节（上游错误文本、冷却原因、最近一次 /quota 错误），不再是拼好的中文句子；`status` 的判定顺序改为 error 优先于 disabled。
- `GET /admin/accounts` 新增 `summary`（与 `/admin/overview` 的 `accounts` 同一结构）。`OverviewAccounts` 新增 `full`、`oversold_count`、`oversold_micro`，账号按显示状态各计入一类（已满的账号从 normal / penalized 里移到 `full`）；`headroom_micro` 仍是 Σ max(0, H_A)，负值不再出现在任何汇总里。
- 分配、换绑、直接开订阅支持 `?dry_run=true`。`SubscriptionWithWarning` 的 `subscription` 变为可空（dry run 时为 null），新增 `dry_run`、`capacity`、`warnings`（`code` + `message`）；`warning` 保留为各条 message 的拼接，已标记废弃。直接开订阅的 dry run 返回 200（正式调用仍是 201）。
- `bridge healthcheck` 子命令与镜像内置 `HEALTHCHECK`（请求 `/readyz`）；Dockerfile 新增 `fakecpa` target，默认 target 仍是 bridge。

### Docker 演示环境

- `docker compose -f docker-compose.demo.yml up --build` 一条命令起 postgres、redis、fakecpa、seed（一次性 `bridge seed-demo`，已有数据时跳过）和 bridge（8080，内置前端）；健康检查与依赖顺序保证 seed 在数据库、Redis、fakecpa 就绪后才跑，bridge 在 seed 成功退出后才启动。`make demo` / `demo-down` / `demo-reset` 只是包装。
- CI 新增 Docker demo job：真实执行这套 compose，等待健康，检查 `/healthz`、管理员登录、`/admin/overview` 账号数大于 0、seed 重复执行无副作用，再对着它跑 Playwright 冒烟测试。

### 前端

- 新 logo：试剂黄方块、2px 墨框、IBM Plex Mono 半粗体的 C₂O₄²⁻（真正的下标与叠排上标，输出为轮廓）和 3px 底线，深浅色下颜色不变；替换侧边栏、手机顶栏与登录页的拱桥图标。新增 SVG favicon、32px PNG 与 180px apple-touch-icon（小尺寸用粗体并收紧字距）。
- 手机端底部标签栏固定在屏幕底部（含安全区），主内容区留出等高空间；「可用额度」在 640px 以下改为每档一张卡片（Plan 两格，普通 / 强制调用 2×2）。
- 账号状态按后端的 `display_status` 显示，「5h 已满 / 7d 已满」使用带黄黑斜纹的新标签，不再显示成「降权中」；所有原因码都有中英文文案，未知原因码显示「其他原因」并附原码。
- 空闲额度为负的账号用危险红显示负数并标「预留超出剩余」；账号页与管理概览的总空闲额度改用后端汇总，并单独显示「超卖账号 N 个，合计超出 $X」。
- 分配、换绑、开订阅对话框选中账号后调用 dry run 显示容量与警告，删除了前端自己的估算。
- 用户概览「近 14 天花费」图随所在行的高度伸展（最小 240px），不再在旁边 Plan 卡片较高时留下空白。
- Playwright：手机视口下标签栏底边贴齐视口、滚到底后最后一个内容块不被遮挡、所有用户侧页面无横向溢出；状态翻译、超卖标注、dry run 预览与 logo 的冒烟测试；截图全部重新生成并新增登录页。

### 后端

- 订阅绑定的容量检查改为 `subs.CheckCapacity`，正式调用与 dry run 在各自事务之后用同一个函数计算，警告完全一致；dry run 在同一事务里执行全部校验后回滚，不写任何数据（含审计日志与余额）。新增警告 `week_total_unknown`、`account_not_normal`。
- `bridge seed-demo` 写入的演示 cpa-quota 估算取自 fakecpa 的同一份配置（`fakecpa.DemoProfiles`）：先刷新桥自己的快照，再读取 fakecpa 当时的 /quota 写入 tier、总额、安全系数、已用、重置时间与剩余，k 取同一 profile；测试保证账号详情的「运行时状态」与「估算明细」数字一致。
- fakecpa 新增 `/_fake/healthz` 与 `-healthcheck` 参数（容器健康检查用）。

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
