// Package relaymsg holds the human-readable messages of the errors the /v1
// relay returns to clients. Only error.message is localized; error.type,
// error.code, the HTTP status and fields such as window / recovers_at stay
// as they are, so clients that branch on the code are not affected. The
// locale is the relay_error_locale runtime setting (zh-CN by default).
package relaymsg

import "strings"

// Locales.
const (
	ZH = "zh-CN"
	EN = "en"
)

// Norm maps a setting value to a supported locale (default zh-CN).
func Norm(locale string) string {
	if strings.HasPrefix(strings.ToLower(locale), "en") {
		return EN
	}
	return ZH
}

// Message ids. Most are the error code they belong to; a few codes have
// several texts (e.g. the plan account being unassigned or unavailable).
const (
	NotFound               = "not_found"
	MissingKey             = "missing_api_key"
	AuthUnavailable        = "auth_unavailable"
	InvalidKey             = "invalid_api_key"
	KeyDisabled            = "key_disabled"
	BodyTooLarge           = "body_too_large"
	KeyLimitsUnavailable   = "key_limits_unavailable"
	UserConcurrencyZero    = "user_concurrency_zero"
	RateLimiterUnavailable = "rate_limiter_unavailable"
	ConcurrencyLimited     = "concurrency_limited"
	RPMLimited             = "rpm_limited"
	UpstreamUnreachable    = "upstream_unreachable"
	StatusLimited          = "status_limited"
	StatusUnavailable      = "status_unavailable"
	SafeguardUnavailable   = "safeguard_unavailable"

	// Key-level refusals.
	KeyExpired            = "key_expired"
	IPNotAllowed          = "ip_not_allowed"
	ModelNotAllowed       = "model_not_allowed"
	KeyQuota              = "key_quota_exhausted"
	KeyWindow             = "key_window_exhausted"
	KeyWindowRecover      = "key_window_exhausted_recover"
	KeyBalanceQuota       = "key_balance_quota_exhausted"
	KeyBalanceWindow      = "key_balance_window_exhausted"
	KeyBalanceWinRecover  = "key_balance_window_exhausted_recover"
	SafeguardLocked       = "safeguard_locked"
	SafeguardPromptBlocks = "safeguard_prompt_blocked"

	// Tier rejections (error.rejections[].message) and their summary.
	NoTier             = "no_tier"
	TierPlan           = "tier_plan"
	TierStandard       = "tier_standard"
	TierForce          = "tier_force"
	NoTiersEnabled     = "no_tiers_enabled"
	NoSubscription     = "no_subscription"
	SubInactive        = "subscription_inactive"
	SubExpired         = "subscription_expired"
	ModelNotInPlan     = "model_not_in_plan"
	PlanUnassigned     = "plan_account_unassigned"
	PlanAccountDown    = "plan_account_unavailable"
	PlanAccountUsedUp  = "plan_account_exhausted"
	PlanModelNotServed = "plan_model_not_served"
	PlanAccountBusy    = "plan_account_busy"
	PlanExhausted      = "plan_quota_exhausted"
	QuotaStateDown     = "quota_state_unavailable"
	BalanceStateDown   = "balance_state_unavailable"
	RouteStateDown     = "route_state_unavailable"
	InsufficientFunds  = "insufficient_balance"
	AccountBusy        = "account_concurrency_full"
	NoAccount          = "no_available_account"
	UnknownTier        = "unsupported_tier"
)

// table: id -> {zh-CN, en}. Placeholders are {name}.
var table = map[string][2]string{
	NotFound: {"本站只提供 Claude Messages API（/v1/messages、/v1/messages/count_tokens、/v1/models、/v1/bridge/status）",
		"only the Claude Messages API is served (/v1/messages, /v1/messages/count_tokens, /v1/models, /v1/bridge/status)"},
	MissingKey:             {"缺少 API key", "missing API key"},
	AuthUnavailable:        {"鉴权服务暂不可用", "authentication temporarily unavailable"},
	InvalidKey:             {"API key 无效", "invalid API key"},
	KeyDisabled:            {"API key 或用户已停用", "API key or user is disabled"},
	BodyTooLarge:           {"请求体过大或无法读取", "request body too large or unreadable"},
	KeyLimitsUnavailable:   {"key 限额状态暂不可用", "key limit state unavailable"},
	UserConcurrencyZero:    {"用户并发上限为 0", "the user's concurrency limit is 0"},
	RateLimiterUnavailable: {"限流服务暂不可用", "rate limiter unavailable"},
	ConcurrencyLimited:     {"并发数已达上限", "concurrency limit reached"},
	RPMLimited:             {"每分钟请求数已达上限", "requests-per-minute limit reached"},
	UpstreamUnreachable:    {"上游不可达", "upstream unreachable"},
	StatusLimited:          {"状态查询每个 key 每分钟最多 {limit} 次", "status requests are limited to {limit} per minute per key"},
	StatusUnavailable:      {"状态暂不可用", "status temporarily unavailable"},
	SafeguardUnavailable:   {"safeguard 状态暂不可用", "safeguard state unavailable"},

	KeyExpired:           {"API key 已过期", "API key has expired"},
	IPNotAllowed:         {"客户端 IP 不在该 key 的 IP 白名单内", "client IP is not in the key's IP allowlist"},
	ModelNotAllowed:      {"该 API key 不允许使用模型 \"{model}\"", "model \"{model}\" is not allowed for this API key"},
	KeyQuota:             {"API key 总额度已用完", "API key quota exhausted"},
	KeyWindow:            {"API key {window} 窗口限额已用完", "API key {window} spending limit reached"},
	KeyWindowRecover:     {"API key {window} 窗口限额已用完，预计 {recovers_at} 恢复", "API key {window} spending limit reached; expected to recover at {recovers_at}"},
	KeyBalanceQuota:      {"key 余额口径的总额度已用完（Plan 档仍可用）", "API key balance quota exhausted; the plan tier is still available"},
	KeyBalanceWindow:     {"key 余额口径的 {window} 窗口限额已用完（Plan 档仍可用）", "API key {window} balance spending limit reached; the plan tier is still available"},
	KeyBalanceWinRecover: {"key 余额口径的 {window} 窗口限额已用完，预计 {recovers_at} 恢复（Plan 档仍可用）", "API key {window} balance spending limit reached; expected to recover at {recovers_at}; the plan tier is still available"},
	SafeguardLocked: {"你的账户触发 safeguard 的次数已用完，所有 API key 已暂停转发。请登录面板使用重置卡或申请解封。触发 safeguard 的请求内容管理员可查看。",
		"Your account has used up its safeguard allowance and all API keys are paused. Sign in to the panel to use a reset card or request an unlock. Administrators can view the requests that triggered the safeguard."},
	SafeguardPromptBlocks: {"这条提示词此前触发过 safeguard，已被本站自动拦截（不会发往上游，也不扣次数）。请修改后再试。",
		"This prompt has triggered the safeguard before and is blocked by this relay (not sent upstream, no allowance used). Please change it and try again."},

	NoTier:             {"没有可用的档位（{details}）", "no tier is available ({details})"},
	TierPlan:           {"plan", "plan"},
	TierStandard:       {"普通调用", "standard"},
	TierForce:          {"强制调用", "force"},
	NoTiersEnabled:     {"key 未开启任何档位", "the key has no tier enabled"},
	NoSubscription:     {"key 未绑定 plan 订阅", "the key is not bound to a plan subscription"},
	SubInactive:        {"plan 订阅未生效", "the plan subscription is not active"},
	SubExpired:         {"plan 订阅已过期", "the plan subscription has expired"},
	ModelNotInPlan:     {"模型不在 plan 内", "the model is not included in the plan"},
	PlanUnassigned:     {"plan 订阅未分配账号", "the plan subscription has no account assigned"},
	PlanAccountDown:    {"plan 绑定的账号不可用", "the plan's account is unavailable"},
	PlanAccountUsedUp:  {"plan 绑定的账号额度已用尽（{why}）", "the plan's account is exhausted ({why})"},
	PlanModelNotServed: {"plan 绑定的账号不提供该模型", "the plan's account does not serve this model"},
	PlanAccountBusy:    {"plan 绑定账号并发已满", "the plan's account is at its concurrency limit"},
	PlanExhausted:      {"plan 本期额度已用完", "the plan's quota for this period is used up"},
	QuotaStateDown:     {"额度状态暂不可用", "quota state unavailable"},
	BalanceStateDown:   {"余额状态暂不可用", "balance state unavailable"},
	RouteStateDown:     {"路由状态暂不可用", "routing state unavailable"},
	InsufficientFunds:  {"余额不足", "insufficient balance"},
	AccountBusy:        {"可用账号并发已满", "every available account is at its concurrency limit"},
	NoAccount:          {"无可用账号", "no account available"},
	UnknownTier:        {"未知档位", "unknown tier"},
}

// IDs lists every message id (tests).
func IDs() []string {
	out := make([]string, 0, len(table))
	for id := range table {
		out = append(out, id)
	}
	return out
}

// Raw returns the two templates of an id (tests).
func Raw(id string) ([2]string, bool) {
	t, ok := table[id]
	return t, ok
}

// Text renders message id in locale. Unknown ids render as the id itself.
func Text(locale, id string, params map[string]string) string {
	t, ok := table[id]
	if !ok {
		return id
	}
	tpl := t[0]
	if Norm(locale) == EN {
		tpl = t[1]
	}
	for k, v := range params {
		tpl = strings.ReplaceAll(tpl, "{"+k+"}", v)
	}
	return tpl
}

// TierLabel is the display name of a tier in locale.
func TierLabel(locale, tier string) string {
	switch tier {
	case "plan":
		return Text(locale, TierPlan, nil)
	case "standard":
		return Text(locale, TierStandard, nil)
	case "force":
		return Text(locale, TierForce, nil)
	}
	return tier
}

// Sep is the separator between the per-tier parts of the no_tier message.
func Sep(locale string) string {
	if Norm(locale) == EN {
		return "; "
	}
	return "；"
}
