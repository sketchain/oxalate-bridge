import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];
export type Tier = Schemas["Tier"];

/** Error codes the backend documents (openapi.yaml, Error.code). */
export const ERROR_CODES = [
  "bad_request",
  "validation_failed",
  "unauthorized",
  "forbidden",
  "csrf_header_missing",
  "not_found",
  "conflict",
  "rate_limited",
  "insufficient_balance",
  "invalid_state",
  "upstream_validation_failed",
  "unsupported_subscription",
  "account_has_subscriptions",
  "cpa_quota_unavailable",
  "internal",
] as const;
export type ErrorCode = (typeof ERROR_CODES)[number];

/** A failed API call. `code` is a documented code, "network" or "unknown". */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode | "network" | "unknown";
  readonly blocking?: Schemas["BlockingSubscription"][];

  constructor(status: number, code: string, message: string, blocking?: Schemas["BlockingSubscription"][]) {
    super(message);
    this.status = status;
    this.code = (ERROR_CODES as readonly string[]).includes(code) || code === "network" ? (code as ErrorCode) : "unknown";
    this.blocking = blocking;
  }
}

type UnauthorizedHandler = () => void;
let onUnauthorized: UnauthorizedHandler = () => {};
/** Registers what happens when a call returns 401 (redirect to login). */
export function setUnauthorizedHandler(fn: UnauthorizedHandler) {
  onUnauthorized = fn;
}

const unauthorized: Middleware = {
  onResponse({ request, response }) {
    if (response.status === 401 && !request.url.endsWith("/auth/login")) onUnauthorized();
    return response;
  },
};

export const api = createClient<paths>({
  baseUrl: "/api/v1",
  credentials: "include",
  headers: { "X-Requested-With": "XMLHttpRequest" },
});
api.use(unauthorized);

type Result<T> = { data?: T; error?: unknown; response: Response };

/** Resolves an openapi-fetch call to its data or throws an ApiError. */
export async function unwrap<T>(call: Promise<Result<T>>): Promise<T> {
  let res: Result<T>;
  try {
    res = await call;
  } catch (e) {
    throw new ApiError(0, "network", e instanceof Error ? e.message : String(e));
  }
  if (res.error !== undefined || !res.response.ok) {
    const body = res.error as Schemas["Error"] | undefined;
    const err = body?.error;
    throw new ApiError(res.response.status, err?.code ?? "unknown", err?.message ?? res.response.statusText, err?.subscriptions);
  }
  return res.data as T;
}
