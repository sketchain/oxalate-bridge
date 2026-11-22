# oxalate-bridge

自建的 Claude API 中转站：用户与 API key、token plan、余额、计费和路由全部由本项目管理，上游是多个 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）v8 实例。每个 CPA 实例只挂一个 Claude 订阅账号（Pro / Max 5x / Max 20x），额度估算来自 CPA 上的 [cpa-quota](https://github.com/sketchain/cpa-quota) 插件。中转站只服务 Claude Messages 协议，只接受 Claude 订阅的上游账号。

本仓库目前是 v0.4.1（第六轮小修正：「仅余额」口径的 key 额度用完后不再拦 Plan 档；公开接口限频可在运行时设置里调）。第五轮（v0.4.0）在第四轮的基础上补齐了完整中转站常用的功能——API key 的额度 / 滑动窗口 / 过期 / 模型与 IP 白名单、免登录的凭 key 查询页、Claude Code 状态栏接口、按 key 用量、兑换码、公告和模型价格页。只装了 Docker 也能用一条命令跑起完整演示（见「只用 Docker 体验」）。界面截图见 [docs/screenshots](docs/screenshots)（例如 [查询页](docs/screenshots/query-light.png)、[key 详情抽屉](docs/screenshots/user-key-drawer-light.png)、[兑换码管理](docs/screenshots/admin-redeem-light.png)、[价格页](docs/screenshots/user-pricing-light.png)）。

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
scripts/gen-logo.py      由 IBM Plex Mono 生成 C₂O₄²⁻ logo 的路径与 favicon（开发用，产物已提交）
docker-compose.demo.yml  只用 Docker 的演示环境（postgres、redis、fakecpa、seed、bridge）
docs/screenshots         登录页与用户侧、管理侧主要页面的浅色 / 深色 / 手机截图
```

## 工作方式

### 请求主链路（/v1/messages、/v1/messages/count_tokens、GET /v1/models）

只服务 Claude Messages 协议。`/v1` 下的其他路径（包括 v0.1 的 `/v1/responses`、`/v1/chat/completions`）返回 Anthropic 格式的 404（`not_found_error`）。对客户端不做任何校验：任何客户端发到 `/v1/messages` 的请求都接受并原样透传，客户端区分由 CPA 侧插件处理。`GET /v1/models` 返回所有启用且未处于 error 的账号可用模型的并集。

1. 鉴权：`Authorization: Bearer` 或 `X-Api-Key`。进程内 L1（10 秒）→ Redis L2（10 分钟）→ PG；key / 用户改动写 outbox，由投递器删除 L2 并 pub/sub 通知所有副本清 L1。
2. 请求体只读一次并原样保留；用 gjson 取 `model`、`stream`、`max_tokens`、`cache_control` 的 1h ttl。
3. 会话标识按 CPA `ExtractSessionInfo` 的优先级提取（移植，未直接 import：直接依赖会让二进制增加约 15 MB），取不到用 key id；只存哈希。
4. key 级别检查（不往下一档切）：过期 → 403 `key_expired`；来源 IP 不在白名单 → 403 `ip_not_allowed`；模型不在白名单 → 403 `model_not_allowed`；总额度用完 → 429 `key_quota_exhausted`；滑动窗口用完 → 429 `key_window_exhausted`（`error.window` 与预计恢复时间 `error.recovers_at`）。错误体是 Anthropic 原生格式，`error.code` 同时写进请求日志的 `error_type`。见下文「API key 限制」。
5. 用户与 key 两个维度的并发（Redis 租约，带过期，进程崩溃自动回收）和 RPM（60 秒滑动窗口），超限 429。
6. 按 key 的 `tiers` 顺序逐档尝试 plan → standard → force（顺序由 key 决定）。只有「这一档不放行」才切下一档，上游报错不切档；全部不放行时 429，`error.rejections` 逐档列出原因。
7. 预扣（Lua 原子操作）：估算 = 请求体字节 / 3 按输入价 + min(max_tokens, 4096) 按输出价，乘档位与模型倍率，再与 `hold_cap`、可用额、key 剩余额度和各窗口剩余额度取小（同一个脚本里原子完成）。剩余 > 0 才放行，最后一笔允许超出。
8. 原样转发给 CPA，绝不改写请求体；响应（含 SSE）逐块原样回给客户端，同时读取 usage。
9. 还没向客户端写出任何字节前：429 → 冷却到 Retry-After / 已用尽窗口的 reset_at / 60 秒；401/403 → 账号置 error；5xx / 网络错误 → 冷却 30 秒；然后同档换号重试（最多 `max_retries` 次，plan 档不换号）。`claude refusal` 开头的 400 不重试、原样透传并记 safeguard 事件。
10. 结算（Lua 原子操作）：退回钱包与 key 的预扣、计入待落库额、把 billed 累加到 key 的两套用量和分钟桶、累加账号分钟桶、续期粘性映射，并把完整结算记录 XADD 到 Redis Stream。

### 实时钱包（余额与 plan 本期额度）

Redis 中每个钱包（用户余额，或某订阅某一期的额度）由三部分构成：

- `base`：PG 已提交的值（余额，或本期 quota − used）及其**版本号**（`users.balance_version` / `plan_usage_periods.version`）；只会被更高版本覆盖。
- `pending`：Redis 已结算、Stream 里尚未落库的金额；每条 Stream 记录落库后按消息 id 只减一次。
- `holds`：在途请求的预扣（ZSET，带过期时间，读的时候先清掉过期的）。

可用额 = base − pending − holds。管理员充值、购买、退款等写操作先提交 PG，再由 outbox 投递「绝对值 + 版本」；消费者落库后也用同样方式同步。三者顺序任意都收敛到 PG − 未落库金额；key 丢失时按 PG 值重建即可。Redis 必须 `maxmemory-policy noeviction` 并开启 AOF。

### API key 限制

每个 key 可以设置（用户和管理员都能改，管理员的修改写审计日志）：

| 字段 | 含义 |
| --- | --- |
| `quota_limit_micro` | 总额度，从上次重置开始计；空 = 不限 |
| `quota_scope` | `all`（默认）：这个 key 产生的所有 billed 都计入，包括 Plan 档消耗的 plan 额度；`balance`：只计入普通 / 强制档扣的余额。同时作用于总额度和时间窗口 |
| `limit_5h_micro` / `limit_1d_micro` / `limit_7d_micro` | 滑动窗口限额：过去 5 小时 / 24 小时 / 7 天内计入口径的 billed 之和（按分钟分桶） |
| `expires_at` | 过期时间 |
| `model_allowlist` | 规范化后的模型名；`/v1/models` 也按它过滤 |
| `ip_allowlist` | IP 或 CIDR，按信任代理配置解析出的真实客户端 IP 判断 |
| `allow_owner_query` | 允许凭此 key 查询账户余额与 Plan 可用额度（查询页、状态栏接口） |

计量沿用钱包的做法：PG `api_keys.used_all_micro / used_balance_micro / usage_version` 是基准，Redis 里是 `kb:<key>`（基准 + 版本）、`kp:<key>`（已结算未落库）、`kh / khv:<key>`（在途预扣）和按分钟的窗口桶 `kwm:<key>:<a|b>:<小时>`（保留 8 天，另有小时合计）。消费者落库时在同一个事务里锁住 key 行、只累加「请求开始时间 ≥ 上次重置时间」的金额，提交后按版本同步回 Redis 并按消息 id 只减一次 pending。Redis 数据丢失时，基准从 PG 重建，窗口桶从最近 7 天的 `request_logs` 重建。

重置（`POST /keys/{id}/reset-usage`、`POST /admin/keys/{id}/reset-usage`）把总额度的已用归零、版本 +1，经 outbox 同步到 Redis；时间窗口本来就是滑动的，不受影响。重置前已结算但还没落库的那几笔（通常 1–2 秒内）落库时会被过滤掉，在此之前查询页可能短暂显示一点已用。

检查顺序：鉴权 → 过期 → IP → 模型白名单 → key 额度与窗口 → 逐档尝试。两种口径额度用完后的行为不同：

- `quota_scope = all`（全部计入）：总额度或任何一个窗口用完，整个 key 拒绝（`key_quota_exhausted` / `key_window_exhausted`，429），不往下一档切。
- `quota_scope = balance`（仅余额）：总额度或某个窗口用完，只把 standard 和 force 从这次请求的档位链里去掉，当作「这一档不放行」，原因码 `key_balance_quota_exhausted`（窗口导致时附 `window` 和 `recovers_at`）；plan 档照常尝试。所有档都不放行时按原规则返回 429，`error.rejections` 逐档列出原因。预扣也一样：plan 档的预扣不受 key 额度和窗口约束，standard / force 的预扣额仍以 key 剩余额度和窗口剩余额度为上限。

`KeyLimitStatus.exhausted`（key 详情、查询页、状态栏接口都有）说明哪一项用完了：`code`、`window`、`recovers_at`，以及 `plan_available`（仅余额口径时为 true，查询页提示「Plan 仍可用」）。

### 凭 key 查询（不用登录）

- 页面：`/query`（登录页有入口）。用密码框输入 key，只存在页面内存里，不写 localStorage，页面上不回显完整 key。
- 接口：`GET /api/v1/public/key-usage`，key 只能放在 `Authorization: Bearer` 或 `x-api-key` 请求头里（URL 参数一律忽略）。按 IP 和按 key 各 30 次/分钟（默认值，管理端「系统设置 → 公开接口限频」可改，保存后所有副本立即生效）。key 无效或已删除时统一 401；已停用或已过期的 key 仍返回信息并带状态。
- 返回 key 名称、前缀、状态、过期时间、额度口径、总额度与已用、三个窗口的限额与已用（窗口用完时带预计恢复时间）、模型白名单和最近 50 条调用（用户视角，不含上游成本）。`allow_owner_query` 为 true 时另外返回账户可用余额、各 Plan 订阅的 5h / 本期可用（口径同 `/me/availability`）和普通 / 强制调用的可用额度；为 false 时 `account` 字段完全不出现。

### 在 Claude Code 状态栏显示余额

转发域名下有 `GET /v1/bridge/status`：鉴权方式和 `/v1/messages` 完全一样（`x-api-key` 或 `Authorization: Bearer`），**不计费、不写 request_logs**，单独限频（每个 key 默认 60 次/分钟，同样在「公开接口限频」里改）。返回紧凑 JSON，字段同上面的查询接口，但不含调用日志：

```json
{
  "generated_at": "2026-11-22T10:31:00Z",
  "key": { "name": "Claude Code", "prefix": "sk-pl…eqh7", "status": "active", "quota_scope": "all", "expires_at": null },
  "limits": {
    "quota_scope": "all", "quota_limit_micro": 50000000, "quota_used_micro": 45800000, "quota_remaining_micro": 4200000,
    "windows": [ { "window": "5h", "limit_micro": 40000000, "used_micro": 50000, "remaining_micro": 39950000 } ]
  },
  "account": { "balance_available_micro": 12300000, "plans": [ { "plan_name": "Max 周卡", "period_available_micro": 88000000 } ] }
}
```

Claude Code 的状态栏（[官方文档](https://code.claude.com/docs/en/statusline)）在 `~/.claude/settings.json`（或项目设置）里配置一个命令：Claude Code 会在会话开始、每条助手消息之后等时机运行它（更新会做 300ms 去抖），把会话 JSON 写到命令的 stdin，命令打到 stdout 的每一行就是状态栏的一行，退出码非 0 或没有输出时状态栏留空；可以用 `refreshInterval`（秒，最小 1）额外定时刷新。脚本运行慢会卡住状态栏更新，所以下面的脚本把结果缓存 30 秒，不会每次刷新都请求中转站。

`~/.claude/settings.json`：

```json
{
  "statusLine": {
    "type": "command",
    "command": "~/.claude/oxalate-status.sh",
    "padding": 0,
    "refreshInterval": 30
  }
}
```

`~/.claude/oxalate-status.sh`（需要 `curl` 和 `jq`，然后 `chmod +x ~/.claude/oxalate-status.sh`）：

```bash
#!/usr/bin/env bash
# Status line for oxalate-bridge. Reads the relay URL and key from the
# environment, caches the answer for ~30 s and prints one short line, e.g.
#   余额 $12.30 · Plan 本期 $88 · Key 剩 $4.20
# OXALATE_BASE_URL  relay base URL, e.g. https://relay.example.com (falls back to ANTHROPIC_BASE_URL)
# OXALATE_API_KEY   the key (falls back to ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN)
set -u
cat >/dev/null # the session JSON on stdin is not needed here
base="${OXALATE_BASE_URL:-${ANTHROPIC_BASE_URL:-}}"
key="${OXALATE_API_KEY:-${ANTHROPIC_API_KEY:-${ANTHROPIC_AUTH_TOKEN:-}}}"
[ -n "$base" ] && [ -n "$key" ] || { echo "oxalate: 未设置地址或 key"; exit 0; }
cache="${TMPDIR:-/tmp}/oxalate-status-$(printf '%s' "$key" | cksum | cut -d' ' -f1).json"
now=$(date +%s)
mtime=$(stat -c %Y "$cache" 2>/dev/null || stat -f %m "$cache" 2>/dev/null || echo 0)
if [ $((now - mtime)) -ge 30 ]; then
  if curl -fsS --max-time 3 -H "x-api-key: $key" "${base%/}/v1/bridge/status" -o "$cache.tmp" 2>/dev/null; then
    mv "$cache.tmp" "$cache"
  else
    rm -f "$cache.tmp"
    touch "$cache" 2>/dev/null # back off 30 s even when the relay is down
  fi
fi
[ -s "$cache" ] || { echo "oxalate: 暂不可用"; exit 0; }
jq -r '
  def usd: (. / 1e6) as $v | if $v >= 100 then "$\($v | floor)" else "$\(($v * 100 | round) / 100)" end;
  [
    (if .account then "余额 \(.account.balance_available_micro | usd)" else empty end),
    (if (.account.plans // []) | map(select(.period_available_micro != null)) | length > 0
       then "Plan 本期 \(.account.plans | map(.period_available_micro // 0) | add | usd)" else empty end),
    (if .limits.quota_remaining_micro != null then "Key 剩 \(.limits.quota_remaining_micro | usd)" else empty end),
    (if .limits.exhausted.plan_available then "Key 余额额度用完（Plan 仍可用）"
     elif .limits.exhausted then "Key 额度用完" else empty end),
    (if .key.status != "active" then "Key \(.key.status)" else empty end)
  ] | if length == 0 then "oxalate 正常" else join(" · ") end
' "$cache" 2>/dev/null || echo "oxalate: 解析失败"
```

把地址和 key 写进环境（例如 `~/.zshrc` 里 `export OXALATE_BASE_URL=https://relay.example.com OXALATE_API_KEY=sk-…`）。账户余额和 Plan 数字只在这个 key 开启了「允许凭此 Key 查询账户」时才有。可以用 `echo '{}' | ~/.claude/oxalate-status.sh` 先在终端里试一下。

### 按 key 用量、兑换码、公告、价格

- `GET /api/v1/keys/{id}/usage/daily?days=N`（默认 30，最大 90）按天、按档位汇总；`GET /api/v1/usage/keys?from=&to=` 给出每个 key 的合计和前 10 个 key 的每日序列。都读小时汇总表 `usage_hourly`（本来就有 key 维度，补了 `(key_id, hour)` 索引），按服务器时区切天。前端：Key 页点开 key 显示详情抽屉；用量页右上角切到「按 Key 对比」。
- 兑换码：管理端「兑换码」页生成批次（面额、数量 ≤ 1000、可选过期时间、前缀、备注），格式为 `前缀-XXXX-XXXX-XXXX-XXXX`（Crockford Base32，80 位随机）。库里只存 sha256（兑换时查找）和加密后的原文（按批次导出 CSV）。用户在「余额与账单」页兑换：一个事务里用条件更新保证一个码只能用一次，写一条 `redeem` 类型的 ledger（幂等键是兑换码 id），经 outbox 同步钱包。每个用户 10 次/分钟，连续失败 5 次锁定 15 分钟。生成、作废、导出都写审计日志。
- 公告：管理端增删改查（标题、纯文本正文、级别普通 / 警告、生效起止时间、启用开关）。用户面板顶部显示当前生效的公告，正文只渲染换行和 http(s) 链接，不解析 HTML；关闭状态按用户记在服务端。
- 模型与价格：`GET /api/v1/pricing` 列出所有启用账号可用模型的并集，每个模型三档各自的每百万 token 价格（输入、输出、缓存读、缓存写）= toll 倍率换算出的单价 × 档位倍率 × 模型倍率；没有定价的模型标 `priced: false`。用户侧和管理侧导航都有「模型与价格」。

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
| `public_query_ip_per_minute` / `public_query_key_per_minute` | 30 / 30 | 凭 key 查询接口按 IP / 按 key 的每分钟次数（1–10000） |
| `status_key_per_minute` | 60 | 状态栏接口 `/v1/bridge/status` 每个 key 的每分钟次数（1–10000） |

## 只用 Docker 体验（演示环境）

本机只需要 Docker（含 Compose v2），不需要 Go、Node。一条命令：

```bash
docker compose -f docker-compose.demo.yml up --build
```

第一次会构建镜像（前端 + Go，几分钟），之后依次启动：postgres、redis、fakecpa（4 个假的 CPA 实例，不连任何真实上游）→
seed（一次性任务，执行 `bridge seed-demo` 写入演示数据后退出；库里已有演示数据时直接跳过）→ bridge。
看到 bridge 变成 healthy 后打开 <http://localhost:8080>（端口被占用时用 `DEMO_PORT=8090 docker compose -f docker-compose.demo.yml up --build`）。

演示账号：

| 用户名 | 密码 | 说明 |
| --- | --- | --- |
| `admin` | `admin-pass-123` | 管理员：概览、上游账号、订阅分配、日志等 |
| `alice` | `demo-pass-123` | 余额 $80（另兑换过一个 $10 兑换码），一份有效的 Max 周卡 + 一份待分配的 Sonnet 轻量包，两个带限额的 key（「Claude Code」开启了凭 key 查询账户） |
| `bob` | `demo-pass-123` | 余额较少，管理员直接开通的 Max 月卡（所在账号已超卖） |
| `carol` | `demo-pass-123` | 几乎没有余额，只有余额档 key |

4 个上游账号分别处于：正常、降权中、5h 已满、几分钟后变成「数据过期」。另有一批 20 个兑换码（前缀 OXA，其中一个已用、一个已作废）和一条公告。凭 key 查询页在 <http://localhost:8080/query>。

停止与重置：

```bash
docker compose -f docker-compose.demo.yml down       # 停止，保留数据（下次 up 不重新生成）
docker compose -f docker-compose.demo.yml down -v    # 停止并删除数据卷；下次 up 重新生成演示数据
```

有 make 的话也可以用 `make demo` / `make demo-down` / `make demo-reset`，与上面三条命令等价，不是必须的。
fakecpa 的额度窗口以容器启动时间为基准，隔了很久再 `up` 时账号的「运行时状态」会与当初写入的「估算明细」逐渐对不上，`down -v` 重置即可。
演示环境的密钥、密码都写死在 `docker-compose.demo.yml` 里，只能用于本地体验，不要用于部署。

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
