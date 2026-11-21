import { Outlet, createFileRoute, redirect } from "@tanstack/react-router";
import { useSuspenseQuery } from "@tanstack/react-query";
import { ApiError } from "@/api/client";
import { meQuery } from "@/api/queries";
import { AppShell } from "@/components/AppShell";

/** Signed-in area: loads the current user or sends the visitor to the login page. */
export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context, location }) => {
    try {
      await context.queryClient.ensureQueryData(meQuery);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        throw redirect({ to: "/login", search: { redirect: location.href } });
      }
      throw e;
    }
  },
  component: AppLayout,
});

function AppLayout() {
  const { data: me } = useSuspenseQuery(meQuery);
  return (
    <AppShell me={me}>
      <Outlet />
    </AppShell>
  );
}
