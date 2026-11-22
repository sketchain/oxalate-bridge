import { createFileRoute } from "@tanstack/react-router";
import { PricingView } from "@/components/PricingView";

export const Route = createFileRoute("/_app/pricing")({ component: PricingPage });

function PricingPage() {
  return <PricingView code="OB-17" />;
}
