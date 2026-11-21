import { Outlet, createFileRoute } from "@tanstack/react-router";
import { useSuspenseQuery } from "@tanstack/react-query";
import { meQuery } from "@/api/queries";
import { Forbidden } from "@/components/RouteStates";
import { useAdminEventSource } from "@/lib/events";

/** Admin area: a 403 page for everyone else, and one SSE connection for live updates. */
export const Route = createFileRoute("/_app/admin")({
  component: AdminLayout,
});

function AdminLayout() {
  const { data: me } = useSuspenseQuery(meQuery);
  const admin = me.role === "admin";
  useAdminEventSource(admin);
  if (!admin) return <Forbidden />;
  return <Outlet />;
}
