# 待办

本文件记录已经确定方向、但还没有排进开发轮次的事项。每项写明目标和要点，动手前再细化成正式需求。

## 1. Scriptable 小组件

**目标**：在 iPhone / iPad 主屏幕上用 [Scriptable](https://scriptable.app/) 小组件随时看到中转站状态——管理员看账号池，普通用户看自己的余额与额度，不用打开面板。

**后端：小组件专用汇总接口**

- 新增一个只读接口（例如 `GET /api/v1/admin/widget`），只接受**只读**管理 API token（`Authorization: Bearer oba-…`，scope = read），会话 Cookie 也可以用于调试。
- 一次请求返回小组件需要的全部数据，不让脚本拼多个接口；返回体尽量小：字段名短、只给展示需要的数值（金额用 micro-USD 整数或已格式化字符串二选一，统一一种），不带账号详情、快照原文。
- 内容至少包括：
  - 账号计数：正常、需要处理（error / stale / cooldown / 已满）的数量；
  - 状态最差的账号（名称、状态、原因）；
  - 最多 N 个账号（按状态从差到好、再按 5h 已用从高到低）的 5h / 7d 已用百分比和状态；
  - 今日花费（服务器时区）、待分配订阅数、总空闲额度（H_A 之和或可分配池子）；
  - 生成时间（脚本据此显示「数据时间」，并判断是否过期）。
- 走 `admin_token_per_minute` 限频；考虑加 `Cache-Control: max-age=30` 和 ETag，减少小组件刷新带来的查询。
- openapi.yaml 标注 `x-admin-token-scope: read`，补测试（只读 token 可用、写 token 也可用、未登录 401）。

**管理员小组件（三种尺寸）**

- 小：正常账号数 / 需要处理的账号数（大号数字），下面一行是状态最差的那个号（名称 + 状态标签）。
- 中：最多 4 个号，每个号一行：名称、5h 和 7d 两条进度条、状态标签。
- 大：中尺寸内容，再加今日花费、待分配订阅数、总空闲额度三项数字。
- 点击小组件打开面板对应页面（管理概览 / 账号页）。

**用户小组件**

- 直接调用 `GET /v1/bridge/status`（API key 鉴权，转发域名，不计费不写日志），不需要新接口。
- 显示：余额、Plan 本期剩余、这个 key 的剩余额度（key 没开「允许凭此 Key 查询账户」时只显示 key 自身的数字，并提示去面板打开）。

**脚本与文档**

- 放在 `scripts/scriptable/`：管理员脚本、用户脚本各一个，配置项（面板地址、token / key）写在文件开头或用 Scriptable 的 widget parameter 传入。
- 视觉沿用「试剂标签」风格：只用黑 / 白 / 黄三色，2px 墨线边框，等宽数字，深色模式跟随系统；状态异常用黄底黑字，不用其他颜色。
- 网络失败时显示上次成功的数据和「数据时间」，并在角落标注离线；token 失效（401）时显示明确的提示。
- `scripts/scriptable/README.md` 写安装方法：安装 Scriptable、新建脚本粘贴代码、在面板创建只读 token、添加小组件并选择脚本 / 填参数、各尺寸效果截图。

## 2. 接入真实 CPA 联调

**目标**：用真实账号跑通全链路，确认演示数据之外的真实数据口径正确。

- 在测试实例上装好 cpa-quota 和 cpa-stego-guard 插件，挂真号（至少两个 Claude 账号），桥按正式配置接入。
- 核对额度数据：`/quota` 的 5h / 7d 已用百分比、总额、重置时间与 Claude 官方显示一致；本地窗口重置、stale 判定、cooldown 在真实节奏下表现正常。
- 核对 k 值：面板显示的 k 与 cpa-quota `/quota` 的 `accounts[0].k`（以及估算表）一致，并观察几天的 k 趋势是否合理。
- 核对缓存命中：Claude Code 长会话下缓存读写 token 的拆分、缓存命中率统计与上游账单一致；1h cache ttl 的会话粘性是否生效。
- 成本对比：用账号详情的 cost-compare 对比 cpa-quota `requests.cost_usd` 与桥的 `upstream_cost`，差异控制在可解释范围（未经过桥的请求、按开始时间分桶等）。
- 验证推送：账号 401/403、stale、额度用满在真实情况下是否按预期只推一次。
- 把发现的问题记成 issue，联调结论写进 README 的「已知限制」或运维说明。

## 3. 正式部署

**目标**：在 VPS 上长期运行，有域名、TLS 和数据备份。

- VPS 上用 docker compose 部署（bridge、PostgreSQL、Redis；cpa-quota 与桥共用 PG），镜像使用 `ghcr.io/sketchain/oxalate-bridge` 的固定版本标签，不用 `latest`。
- 域名与 TLS：反向代理（Caddy 或 Nginx）自动签发证书，面板与转发可以同域或分两个子域；正确设置 `BRIDGE_TRUSTED_PROXIES`、`BRIDGE_COOKIE_SECURE=true`；`BRIDGE_VAPID_SUBJECT` 填真实联系邮箱。
- 密钥：`BRIDGE_SECRET_KEY` 等放在 `.env`（权限 600，不进仓库）并离线备份——丢失后已加密的上游密钥和 VAPID 私钥都无法解密。
- Redis：`maxmemory-policy noeviction`，开启 AOF。
- PG 定时备份：每天 `pg_dump`（含 bridge 与 cpa-quota 两个 schema）压缩后保留最近 N 天，同步一份到异地（对象存储）；写好恢复步骤并实际演练一次。
- 监控：`/healthz`、`/readyz` 接入外部探活；`/metrics` 只在内网暴露，按需接 Prometheus / Grafana（可以用只读管理 API token 拉面板数据）。
- 升级流程：先备份，再拉新镜像滚动重启（先停旧副本再起新副本，见各版本 Changelog）。
