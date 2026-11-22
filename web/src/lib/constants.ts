import type { Schemas, Tier } from "@/api/client";

export const LEDGER_TYPES: Schemas["LedgerType"][] = ["admin_topup", "admin_adjust", "plan_purchase", "plan_renewal", "api_charge", "refund", "redeem"];
export const SUB_STATUSES: Schemas["SubscriptionStatus"][] = ["pending_assignment", "active", "expired", "cancelled"];
export const TIER_LIST: Tier[] = ["plan", "standard", "force"];

/** Relay error types of key-level rejections (request log error_type). */
export const KEY_ERROR_TYPES = ["key_expired", "ip_not_allowed", "model_not_allowed", "key_quota_exhausted", "key_window_exhausted"] as const;
