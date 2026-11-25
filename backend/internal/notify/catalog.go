// Package notify sends Web Push notifications: VAPID keys, per-user
// preferences, state-change detection, deduplication across replicas and an
// asynchronous delivery queue with retries.
package notify

import (
	"fmt"
	"strings"
	"time"
)

// Event types.
const (
	AccountAuthError    = "account_auth_error"
	AccountStale        = "account_stale"
	SubscriptionPending = "subscription_pending"
	ReconcileMismatch   = "reconcile_mismatch"
	StreamBacklogHigh   = "stream_backlog_high"
	AccountFull         = "account_full"
	SafeguardEvent      = "safeguard_event"
	BalanceLow          = "balance_low"
	PlanExpiring        = "plan_expiring"
	AutoRenewFailed     = "auto_renew_failed"
	KeyExhausted        = "key_exhausted"

	// testEvent is the "send a test notification" button (never stored).
	testEvent = "test"
)

// Groups: admin events go to every active admin, user events to the owner.
const (
	GroupAdmin = "admin"
	GroupUser  = "user"
)

// Spec describes one event type.
type Spec struct {
	Event          string
	Group          string
	DefaultEnabled bool
	// High urgency (account problems).
	High bool
}

// Catalog lists every event in display order.
var Catalog = []Spec{
	{AccountAuthError, GroupAdmin, true, true},
	{AccountStale, GroupAdmin, true, true},
	{SubscriptionPending, GroupAdmin, true, false},
	{ReconcileMismatch, GroupAdmin, true, false},
	{StreamBacklogHigh, GroupAdmin, true, false},
	{AccountFull, GroupAdmin, false, false},
	{SafeguardEvent, GroupAdmin, false, false},
	{BalanceLow, GroupUser, true, false},
	{PlanExpiring, GroupUser, true, false},
	{AutoRenewFailed, GroupUser, true, false},
	{KeyExhausted, GroupUser, false, false},
}

// SpecOf returns the spec of an event.
func SpecOf(event string) (Spec, bool) {
	for _, s := range Catalog {
		if s.Event == event {
			return s, true
		}
	}
	return Spec{}, false
}

// Visible reports whether a user with role can receive an event: admin
// events are for admins only, user events for everyone (admins can own keys
// and balances too).
func Visible(s Spec, role string) bool {
	return s.Group == GroupUser || role == "admin"
}

// DefaultBalanceThreshold is the balance_low threshold when the user has not
// set one ($5).
const DefaultBalanceThreshold int64 = 5_000_000

// PlanExpiringLead is how long before expiry plan_expiring fires.
const PlanExpiringLead = 72 * time.Hour

// BacklogHold is how long the backlog must stay above the threshold.
const BacklogHold = 5 * time.Minute

// ---- texts ------------------------------------------------------------------

type text struct{ title, body string }

var texts = map[string]map[string]text{
	"zh-CN": {
		AccountAuthError:    {"账号异常：{name}", "原因：{cause}。账号已停止调度。{reason}"},
		AccountStale:        {"账号数据过期：{name}", "额度数据已经 {age} 没有更新，账号暂不参与调度。"},
		AccountFull:         {"账号额度用满：{name}", "{window} 窗口已用满，恢复前不再分配新请求。"},
		SubscriptionPending: {"有新的待分配订阅", "{user} 购买了「{plan}」，等待分配账号。"},
		ReconcileMismatch:   {"对账不一致", "余额不一致的用户 {users} 个，扣费差额 {diff}。"},
		StreamBacklogHigh:   {"结算积压过高", "结算队列积压 {backlog} 条，已持续 5 分钟以上（阈值 {threshold}）。"},
		SafeguardEvent:      {"Safeguard 拦截", "{user} 的请求被拦截（{category}）。"},
		BalanceLow:          {"余额低于 {threshold}", "当前可用余额 {balance}，按量档位可能很快无法使用。"},
		PlanExpiring:        {"Plan 即将到期", "「{plan}」将于 {date} 到期。{renew}"},
		AutoRenewFailed:     {"自动续费失败", "「{plan}」自动续费没有成功（{reason}），将于 {date} 到期。"},
		KeyExhausted:        {"Key 额度用完：{key}", "{what}{recover}"},
		testEvent:           {"测试通知", "看到这条消息，说明本设备的推送已经开通。"},
	},
	"en": {
		AccountAuthError:    {"Account error: {name}", "Cause: {cause}. The account is out of routing. {reason}"},
		AccountStale:        {"Account data stale: {name}", "Quota data has not been updated for {age}; the account is out of routing."},
		AccountFull:         {"Account quota full: {name}", "The {window} window is used up; no new requests until it recovers."},
		SubscriptionPending: {"New subscription to assign", "{user} bought \"{plan}\"; it is waiting for an account."},
		ReconcileMismatch:   {"Reconcile mismatch", "{users} user balance(s) differ, charge difference {diff}."},
		StreamBacklogHigh:   {"Settlement backlog high", "{backlog} entries waiting for more than 5 minutes (threshold {threshold})."},
		SafeguardEvent:      {"Safeguard block", "A request of {user} was blocked ({category})."},
		BalanceLow:          {"Balance below {threshold}", "Available balance is {balance}; balance tiers may stop working soon."},
		PlanExpiring:        {"Plan expiring soon", "\"{plan}\" expires on {date}. {renew}"},
		AutoRenewFailed:     {"Auto-renew failed", "\"{plan}\" could not be renewed ({reason}); it expires on {date}."},
		KeyExhausted:        {"Key quota used up: {key}", "{what}{recover}"},
		testEvent:           {"Test notification", "If you can read this, push notifications work on this device."},
	},
}

// localized phrases used as parameters.
var phrases = map[string]map[string]string{
	"zh-CN": {
		"renew_on":       "已开启自动续费，到期前会自动扣费。",
		"renew_off":      "未开启自动续费，到期后 Plan 档将停止使用。",
		"window_5h":      "5 小时",
		"window_week":    "7 天",
		"window_1d":      "1 天",
		"window_7d":      "7 天",
		"key_quota":      "总额度已用完。",
		"key_window":     "{window} 窗口额度已用完。",
		"key_recover":    "预计 {date} 恢复。",
		"renew_balance":  "余额不足",
		"renew_other":    "续费出错",
		"unnamed_key":    "未命名",
		"unknown_reason": "",
		"auth_401":       "凭证失效（401）",
		"auth_sub":       "订阅类型不再是 claude",
		"auth_other":     "其他",
	},
	"en": {
		"renew_on":       "Auto-renew is on; it will be charged before it expires.",
		"renew_off":      "Auto-renew is off; the plan tier stops working after it expires.",
		"window_5h":      "5-hour",
		"window_week":    "7-day",
		"window_1d":      "1-day",
		"window_7d":      "7-day",
		"key_quota":      "The total quota is used up.",
		"key_window":     "The {window} window is used up.",
		"key_recover":    " Expected to recover at {date}.",
		"renew_balance":  "insufficient balance",
		"renew_other":    "renewal error",
		"unnamed_key":    "unnamed",
		"unknown_reason": "",
		"auth_401":       "credentials invalid (401)",
		"auth_sub":       "the subscription is no longer claude",
		"auth_other":     "other",
	},
}

// AuthCause classifies an account's error_reason for account_auth_error:
// "401" (credentials invalid), "sub" (the subscription is no longer claude)
// or "other" (403 and anything else; the raw reason follows in the body).
func AuthCause(reason string) string {
	switch {
	case strings.HasPrefix(reason, "upstream 401"):
		return "401"
	case strings.HasPrefix(reason, "unsupported subscription"):
		return "sub"
	}
	return "other"
}

// NormLocale maps any value to a supported locale (default zh-CN).
func NormLocale(l string) string {
	if strings.HasPrefix(strings.ToLower(l), "en") {
		return "en"
	}
	return "zh-CN"
}

func phrase(locale, key string, params map[string]string) string {
	return fill(phrases[NormLocale(locale)][key], params)
}

func fill(tpl string, params map[string]string) string {
	for k, v := range params {
		tpl = strings.ReplaceAll(tpl, "{"+k+"}", v)
	}
	return strings.TrimSpace(tpl)
}

// Render returns the localized title and body of an event.
func Render(event, locale string, params map[string]string) (string, string) {
	t, ok := texts[NormLocale(locale)][event]
	if !ok {
		return event, ""
	}
	return fill(t.title, params), fill(t.body, params)
}

// usd formats micro-USD for notification texts.
func usd(micro int64) string {
	return fmt.Sprintf("$%.2f", float64(micro)/1e6)
}

func fmtDate(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

func fmtAge(d time.Duration, locale string) string {
	m := int(d.Minutes())
	if NormLocale(locale) == "en" {
		if m < 60 {
			return fmt.Sprintf("%d min", max(m, 1))
		}
		return fmt.Sprintf("%.1f h", d.Hours())
	}
	if m < 60 {
		return fmt.Sprintf("%d 分钟", max(m, 1))
	}
	return fmt.Sprintf("%.1f 小时", d.Hours())
}
