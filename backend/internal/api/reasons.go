package api

import "strings"

// Account status reason codes (AccountRuntime.status_reason_code). The panel
// translates every code; TestReasonCodesTranslated keeps the locales in step.
const (
	ReasonDisabled      = "disabled"
	ReasonUpstreamAuth  = "upstream_auth"
	ReasonError         = "error"
	ReasonNetwork       = "network"
	ReasonUpstream429   = "upstream_429"
	ReasonUpstream5xx   = "upstream_5xx"
	ReasonErrorCooldown = "error_cooldown"
	ReasonCooldown      = "cooldown"
	ReasonQuotaStale    = "quota_stale"
	Reason5hExhausted   = "5h_exhausted"
	ReasonWeekExhausted = "week_exhausted"
	ReasonPenalized     = "penalized"
)

// ReasonCodes are all the codes the backend produces.
var ReasonCodes = []string{
	ReasonDisabled, ReasonUpstreamAuth, ReasonError, ReasonNetwork, ReasonUpstream429,
	ReasonUpstream5xx, ReasonErrorCooldown, ReasonCooldown, ReasonQuotaStale, Reason5hExhausted, ReasonWeekExhausted,
	ReasonPenalized,
}

// errorReasonCode classifies an account's stored error_reason (written by
// the relay on upstream 401 / 403).
func errorReasonCode(raw string) string {
	switch {
	case strings.HasPrefix(raw, "upstream 401"), strings.HasPrefix(raw, "upstream 403"):
		return ReasonUpstreamAuth
	}
	return ReasonError
}

// cooldownReasonCode classifies a cooldown marker's reason (see the relay's
// failure handling).
func cooldownReasonCode(raw string) string {
	switch {
	case raw == "network":
		return ReasonNetwork
	case raw == "upstream 429":
		return ReasonUpstream429
	case strings.HasPrefix(raw, "upstream 5"):
		return ReasonUpstream5xx
	case strings.HasPrefix(raw, "error: "):
		return ReasonErrorCooldown
	}
	return ReasonCooldown
}

// hardDisplay maps a hard-exhaustion reason to its display status; ok is
// false for reasons that are not a full window (cooldown).
func hardDisplay(reason string) (AccountDisplayStatus, bool) {
	switch reason {
	case Reason5hExhausted:
		return AccountDisplayStatusFiveHourFull, true
	case ReasonWeekExhausted:
		return AccountDisplayStatusWeekFull, true
	}
	return "", false
}

// displayOf picks the single display status by priority: error > disabled
// > cooldown > full (5h / week) > stale > penalized > normal, and
// the matching reason code and raw detail.
func displayOf(enabled, errored bool, errorReason string, cooldown *string, hardReason string, stale bool, lastError string,
	penalized bool) (AccountDisplayStatus, string, string) {
	switch {
	case errored:
		return AccountDisplayStatusError, errorReasonCode(errorReason), errorReason
	case !enabled:
		return AccountDisplayStatusDisabled, ReasonDisabled, ""
	case cooldown != nil:
		return AccountDisplayStatusCooldown, cooldownReasonCode(*cooldown), *cooldown
	}
	if d, ok := hardDisplay(hardReason); ok {
		return d, hardReason, ""
	}
	switch {
	case stale:
		return AccountDisplayStatusStale, ReasonQuotaStale, lastError
	case penalized:
		return AccountDisplayStatusPenalized, ReasonPenalized, ""
	}
	return AccountDisplayStatusNormal, "", ""
}
