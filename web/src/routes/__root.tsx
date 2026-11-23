import { Outlet, createRootRouteWithContext } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import { Toaster } from "@/components/ui/sonner";
import { IosInstallBanner, UpdateBanner } from "@/components/PwaBanners";

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: () => (
    <>
      <Outlet />
      <IosInstallBanner />
      <UpdateBanner />
      <Toaster />
    </>
  ),
});
