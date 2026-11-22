import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import "./styles.css";
import "./i18n";
import { routeTree } from "./routeTree.gen";
import { ApiError, setUnauthorizedHandler } from "./api/client";
import { applyTheme, getTheme, watchSystemTheme } from "./lib/theme";
import { RouteError, RoutePending, NotFound } from "./components/RouteStates";
import { captureInstallPrompt, registerServiceWorker } from "./lib/pwa";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      gcTime: 10 * 60_000,
      refetchOnWindowFocus: true,
      // Client errors (4xx) are final; network and 5xx errors retry twice.
      retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2,
    },
  },
  queryCache: new QueryCache(),
});

export const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: "intent",
  defaultPreloadStaleTime: 0,
  scrollRestoration: true,
  defaultPendingComponent: RoutePending,
  defaultErrorComponent: RouteError,
  defaultNotFoundComponent: NotFound,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

setUnauthorizedHandler(() => {
  const loc = router.state.location;
  if (loc.pathname === "/login") return;
  queryClient.clear();
  void router.navigate({ to: "/login", search: { redirect: loc.href }, replace: true });
});

applyTheme(getTheme());
watchSystemTheme();
captureInstallPrompt();
registerServiceWorker();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
