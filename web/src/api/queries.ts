import { queryOptions, infiniteQueryOptions, type QueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import type { operations } from "./schema";

type Q<O extends keyof operations> = operations[O] extends { parameters: { query?: infer P } } ? NonNullable<P> : never;

/** Query keys. Everything a user sees lives under "me"; admin data under "admin". */
export const keys = {
  me: ["me"] as const,
  availability: ["me", "availability"] as const,
  balance: ["me", "balance"] as const,
  keys: ["me", "keys"] as const,
  keySecret: (id: string) => ["me", "keys", id, "secret"] as const,
  ledger: (f: Q<"listMyLedger">) => ["me", "ledger", f] as const,
  subscriptions: ["me", "subscriptions"] as const,
  plansOnSale: ["plans", "on-sale"] as const,
  usageLogs: (f: Q<"listMyUsageLogs">) => ["me", "usage", "logs", f] as const,
  usageSummary: (f: Q<"getMyUsageSummary">) => ["me", "usage", "summary", f] as const,
  models: ["models"] as const,
  keyLimits: (id: string) => ["me", "keys", id, "limits"] as const,
  keyDaily: (id: string, days: number) => ["me", "keys", id, "daily", days] as const,
  keyCompare: (f: Q<"getMyKeyUsage">) => ["me", "usage", "keys", f] as const,
  announcements: ["me", "announcements"] as const,
  pricing: ["pricing"] as const,

  admin: ["admin"] as const,
  overview: ["admin", "overview"] as const,
  users: (search: string) => ["admin", "users", "list", search] as const,
  user: (id: string) => ["admin", "users", id] as const,
  userKeys: (id: string) => ["admin", "users", id, "keys"] as const,
  accounts: ["admin", "accounts"] as const,
  account: (id: string) => ["admin", "accounts", id] as const,
  accountEstimate: (id: string) => ["admin", "accounts", id, "estimate"] as const,
  accountKTrend: (id: string, days: number) => ["admin", "accounts", id, "k-trend", days] as const,
  accountQuota: (id: string, window: "5h" | "7d") => ["admin", "accounts", id, "quota", window] as const,
  accountCost: (id: string, f: Q<"adminGetAccountCostCompare">) => ["admin", "accounts", id, "cost", f] as const,
  plans: ["admin", "plans"] as const,
  adminSubscriptions: (f: Q<"adminListSubscriptions">) => ["admin", "subscriptions", f] as const,
  settings: ["admin", "settings"] as const,
  prices: ["admin", "prices"] as const,
  requestLogs: (f: Q<"adminListRequestLogs">) => ["admin", "request-logs", f] as const,
  adminUsage: (f: Q<"adminGetUsageSummary">) => ["admin", "usage", f] as const,
  adminLedger: (f: Q<"adminListLedger">) => ["admin", "ledger", f] as const,
  audit: (f: Q<"adminListAuditLogs">) => ["admin", "audit", f] as const,
  safeguard: (f: Q<"adminListSafeguardEvents">) => ["admin", "safeguard", f] as const,
  adminKeyLimits: (id: string) => ["admin", "keys", id, "limits"] as const,
  redeemBatches: ["admin", "redeem", "batches"] as const,
  redeemCodes: (f: Omit<Q<"adminListRedeemCodes">, "cursor">) => ["admin", "redeem", "codes", f] as const,
  adminAnnouncements: ["admin", "announcements"] as const,
};

// ---- user ------------------------------------------------------------------------

export const meQuery = queryOptions({
  queryKey: keys.me,
  queryFn: () => unwrap(api.GET("/me")),
  staleTime: 60_000,
  retry: false,
});

export const availabilityQuery = queryOptions({
  queryKey: keys.availability,
  queryFn: () => unwrap(api.GET("/me/availability")),
  refetchInterval: 30_000,
});

export const balanceQuery = queryOptions({ queryKey: keys.balance, queryFn: () => unwrap(api.GET("/balance")) });
export const keysQuery = queryOptions({ queryKey: keys.keys, queryFn: () => unwrap(api.GET("/keys")) });
export const keySecretQuery = (id: string) =>
  queryOptions({
    queryKey: keys.keySecret(id),
    queryFn: () => unwrap(api.GET("/keys/{keyId}/secret", { params: { path: { keyId: id } } })),
    staleTime: Infinity,
    gcTime: 5 * 60_000,
  });
export const subscriptionsQuery = queryOptions({ queryKey: keys.subscriptions, queryFn: () => unwrap(api.GET("/subscriptions")) });
export const plansOnSaleQuery = queryOptions({ queryKey: keys.plansOnSale, queryFn: () => unwrap(api.GET("/plans")) });
export const modelsQuery = queryOptions({ queryKey: keys.models, queryFn: () => unwrap(api.GET("/models")), staleTime: 5 * 60_000 });

export const keyLimitsQuery = (id: string) =>
  queryOptions({
    queryKey: keys.keyLimits(id),
    queryFn: () => unwrap(api.GET("/keys/{keyId}/limits", { params: { path: { keyId: id } } })),
    refetchInterval: 30_000,
  });
export const keyDailyQuery = (id: string, days: number) =>
  queryOptions({
    queryKey: keys.keyDaily(id, days),
    queryFn: () => unwrap(api.GET("/keys/{keyId}/usage/daily", { params: { path: { keyId: id }, query: { days } } })),
  });
export const keyCompareQuery = (f: Q<"getMyKeyUsage">) =>
  queryOptions({ queryKey: keys.keyCompare(f), queryFn: () => unwrap(api.GET("/usage/keys", { params: { query: f } })) });
export const announcementsQuery = queryOptions({
  queryKey: keys.announcements,
  queryFn: () => unwrap(api.GET("/announcements")),
  staleTime: 60_000,
  refetchInterval: 5 * 60_000,
});
export const pricingQuery = queryOptions({ queryKey: keys.pricing, queryFn: () => unwrap(api.GET("/pricing")), staleTime: 60_000 });

export const usageSummaryQuery = (f: Q<"getMyUsageSummary">) =>
  queryOptions({ queryKey: keys.usageSummary(f), queryFn: () => unwrap(api.GET("/usage/summary", { params: { query: f } })) });

export const usageLogsInfinite = (f: Omit<Q<"listMyUsageLogs">, "cursor">) =>
  infiniteQueryOptions({
    queryKey: keys.usageLogs(f),
    queryFn: ({ pageParam }) => unwrap(api.GET("/usage/logs", { params: { query: { ...f, cursor: pageParam || undefined } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

export const ledgerInfinite = (f: Omit<Q<"listMyLedger">, "cursor">) =>
  infiniteQueryOptions({
    queryKey: keys.ledger(f),
    queryFn: ({ pageParam }) => unwrap(api.GET("/ledger", { params: { query: { ...f, cursor: pageParam || undefined } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

// ---- admin -----------------------------------------------------------------------

export const overviewQuery = queryOptions({
  queryKey: keys.overview,
  queryFn: () => unwrap(api.GET("/admin/overview")),
  refetchInterval: 30_000,
});

export const usersInfinite = (search: string) =>
  infiniteQueryOptions({
    queryKey: keys.users(search),
    queryFn: ({ pageParam }) =>
      unwrap(api.GET("/admin/users", { params: { query: { search: search || undefined, cursor: pageParam || undefined } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

export const userQuery = (id: string) =>
  queryOptions({ queryKey: keys.user(id), queryFn: () => unwrap(api.GET("/admin/users/{userId}", { params: { path: { userId: id } } })) });
export const userKeysQuery = (id: string) =>
  queryOptions({
    queryKey: keys.userKeys(id),
    queryFn: () => unwrap(api.GET("/admin/users/{userId}/keys", { params: { path: { userId: id } } })),
  });

export const accountsQuery = queryOptions({ queryKey: keys.accounts, queryFn: () => unwrap(api.GET("/admin/accounts")) });
export const accountQuery = (id: string) =>
  queryOptions({
    queryKey: keys.account(id),
    queryFn: () => unwrap(api.GET("/admin/accounts/{accountId}", { params: { path: { accountId: id } } })),
  });
export const accountEstimateQuery = (id: string) =>
  queryOptions({
    queryKey: keys.accountEstimate(id),
    queryFn: () => unwrap(api.GET("/admin/accounts/{accountId}/estimate", { params: { path: { accountId: id } } })),
    retry: false,
  });
export const accountKTrendQuery = (id: string, days: number) =>
  queryOptions({
    queryKey: keys.accountKTrend(id, days),
    queryFn: () => unwrap(api.GET("/admin/accounts/{accountId}/k-trend", { params: { path: { accountId: id }, query: { days } } })),
    retry: false,
  });
export const accountQuotaQuery = (id: string, window: "5h" | "7d") =>
  queryOptions({
    queryKey: keys.accountQuota(id, window),
    queryFn: () =>
      unwrap(api.GET("/admin/accounts/{accountId}/quota-series", { params: { path: { accountId: id }, query: { window } } })),
    retry: false,
  });
export const accountCostQuery = (id: string, f: Q<"adminGetAccountCostCompare">) =>
  queryOptions({
    queryKey: keys.accountCost(id, f),
    queryFn: () => unwrap(api.GET("/admin/accounts/{accountId}/cost-compare", { params: { path: { accountId: id }, query: f } })),
    retry: false,
  });

export const adminPlansQuery = queryOptions({ queryKey: keys.plans, queryFn: () => unwrap(api.GET("/admin/plans")) });

export const subscriptionsInfinite = (f: Omit<Q<"adminListSubscriptions">, "cursor">) =>
  infiniteQueryOptions({
    queryKey: keys.adminSubscriptions(f),
    queryFn: ({ pageParam }) => unwrap(api.GET("/admin/subscriptions", { params: { query: { ...f, cursor: pageParam || undefined } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

export const settingsQuery = queryOptions({ queryKey: keys.settings, queryFn: () => unwrap(api.GET("/admin/settings")) });
export const pricesQuery = queryOptions({ queryKey: keys.prices, queryFn: () => unwrap(api.GET("/admin/prices")) });

export const requestLogsInfinite = (f: Omit<Q<"adminListRequestLogs">, "cursor">) =>
  infiniteQueryOptions({
    queryKey: keys.requestLogs(f),
    queryFn: ({ pageParam }) => unwrap(api.GET("/admin/request-logs", { params: { query: { ...f, cursor: pageParam || undefined } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

export const adminUsageQuery = (f: Q<"adminGetUsageSummary">) =>
  queryOptions({ queryKey: keys.adminUsage(f), queryFn: () => unwrap(api.GET("/admin/usage/summary", { params: { query: f } })) });

export const adminLedgerInfinite = (f: Omit<Q<"adminListLedger">, "cursor">) =>
  infiniteQueryOptions({
    queryKey: keys.adminLedger(f),
    queryFn: ({ pageParam }) => unwrap(api.GET("/admin/ledger", { params: { query: { ...f, cursor: pageParam || undefined } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

export const auditInfinite = (f: Omit<Q<"adminListAuditLogs">, "cursor">) =>
  infiniteQueryOptions({
    queryKey: keys.audit(f),
    queryFn: ({ pageParam }) => unwrap(api.GET("/admin/audit-logs", { params: { query: { ...f, cursor: pageParam || undefined } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

export const safeguardInfinite = (f: Omit<Q<"adminListSafeguardEvents">, "cursor">) =>
  infiniteQueryOptions({
    queryKey: keys.safeguard(f),
    queryFn: ({ pageParam }) =>
      unwrap(api.GET("/admin/safeguard-events", { params: { query: { ...f, cursor: pageParam || undefined } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

export const adminKeyLimitsQuery = (id: string) =>
  queryOptions({
    queryKey: keys.adminKeyLimits(id),
    queryFn: () => unwrap(api.GET("/admin/keys/{keyId}/limits", { params: { path: { keyId: id } } })),
  });

export const redeemBatchesInfinite = infiniteQueryOptions({
  queryKey: keys.redeemBatches,
  queryFn: ({ pageParam }) => unwrap(api.GET("/admin/redeem-batches", { params: { query: { cursor: pageParam || undefined } } })),
  initialPageParam: "",
  getNextPageParam: (last) => last.next_cursor ?? undefined,
});

export const redeemCodesInfinite = (f: Omit<Q<"adminListRedeemCodes">, "cursor">) =>
  infiniteQueryOptions({
    queryKey: keys.redeemCodes(f),
    queryFn: ({ pageParam }) => unwrap(api.GET("/admin/redeem-codes", { params: { query: { ...f, cursor: pageParam || undefined } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

export const adminAnnouncementsQuery = queryOptions({ queryKey: keys.adminAnnouncements, queryFn: () => unwrap(api.GET("/admin/announcements")) });

// ---- cache helpers ------------------------------------------------------------------

/** Replaces one account in the cached list and its detail entry. */
export function putAccount(qc: QueryClient, account: Schemas["Account"]) {
  qc.setQueryData(keys.accounts, (old: Schemas["AccountList"] | undefined) =>
    old ? { items: old.items.map((a) => (a.id === account.id ? account : a)) } : old,
  );
  qc.setQueryData(keys.account(account.id), account);
}

/** Replaces one key in the cached list. */
export function putKey(qc: QueryClient, key: Schemas["ApiKey"]) {
  qc.setQueryData(keys.keys, (old: Schemas["ApiKeyList"] | undefined) =>
    old ? { items: old.items.map((k) => (k.id === key.id ? key : k)) } : old,
  );
}

export type { Q };
