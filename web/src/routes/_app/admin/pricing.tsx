import { createFileRoute } from "@tanstack/react-router";
import { PricingView } from "@/components/PricingView";

export const Route = createFileRoute("/_app/admin/pricing")({ component: AdminPricingPage });

function AdminPricingPage() {
  return <PricingView code="OB-20" />;
}
