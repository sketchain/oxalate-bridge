# Changelog

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
