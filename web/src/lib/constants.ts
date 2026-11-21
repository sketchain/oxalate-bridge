import type { Schemas, Tier } from "@/api/client";

export const LEDGER_TYPES: Schemas["LedgerType"][] = ["admin_topup", "admin_adjust", "plan_purchase", "plan_renewal", "api_charge", "refund"];
export const SUB_STATUSES: Schemas["SubscriptionStatus"][] = ["pending_assignment", "active", "expired", "cancelled"];
export const TIER_LIST: Tier[] = ["plan", "standard", "force"];
