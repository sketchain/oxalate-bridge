import { useMutation, type UseMutationOptions } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { errorText } from "./errors";

/**
 * useMutation that reports failures as a toast with the mapped error text
 * (never the raw backend message) and an optional success toast.
 */
export function useApiMutation<TData, TVars = void, TCtx = unknown>(
  opts: UseMutationOptions<TData, Error, TVars, TCtx> & { success?: string; silentError?: boolean },
) {
  const { t } = useTranslation();
  const { success, silentError, onError, onSuccess, ...rest } = opts;
  return useMutation<TData, Error, TVars, TCtx>({
    ...rest,
    onError: (err, vars, ctx, mctx) => {
      if (!silentError) toast.error(errorText(t, err));
      onError?.(err, vars, ctx, mctx);
    },
    onSuccess: (data, vars, ctx, mctx) => {
      if (success) toast.success(success);
      return onSuccess?.(data, vars, ctx, mctx);
    },
  });
}
