import type { LucideIcon } from "lucide-react";
import {
  BookText,
  CreditCard,
  FileClock,
  Gauge,
  KeyRound,
  LayoutDashboard,
  ListOrdered,
  Package,
  ReceiptText,
  ScrollText,
  Server,
  Settings,
  ShieldAlert,
  SlidersHorizontal,
  Ticket,
  Users,
} from "lucide-react";

export interface NavItem {
  /** Two-digit number shown before the label and used as the page code. */
  n: string;
  to: string;
  label: string; // i18n key
  icon: LucideIcon;
  exact?: boolean;
  badge?: "pending";
}

export const userNav: NavItem[] = [
  { n: "01", to: "/", label: "nav.overview", icon: LayoutDashboard, exact: true },
  { n: "02", to: "/keys", label: "nav.keys", icon: KeyRound },
  { n: "03", to: "/usage", label: "nav.usage", icon: Gauge },
  { n: "04", to: "/plans", label: "nav.plans", icon: Ticket },
  { n: "05", to: "/billing", label: "nav.billing", icon: CreditCard },
  { n: "06", to: "/settings", label: "nav.settings", icon: Settings },
];

export const adminNav: NavItem[] = [
  { n: "07", to: "/admin", label: "nav.adminOverview", icon: LayoutDashboard, exact: true },
  { n: "08", to: "/admin/users", label: "nav.users", icon: Users },
  { n: "09", to: "/admin/accounts", label: "nav.accounts", icon: Server },
  { n: "10", to: "/admin/plans", label: "nav.adminPlans", icon: Package },
  { n: "11", to: "/admin/subscriptions", label: "nav.subscriptions", icon: ListOrdered, badge: "pending" },
  { n: "12", to: "/admin/logs", label: "nav.requestLogs", icon: ScrollText },
  { n: "13", to: "/admin/ledger", label: "nav.ledger", icon: ReceiptText },
  { n: "14", to: "/admin/audit", label: "nav.audit", icon: FileClock },
  { n: "15", to: "/admin/safeguard", label: "nav.safeguard", icon: ShieldAlert },
  { n: "16", to: "/admin/settings", label: "nav.adminSettings", icon: SlidersHorizontal },
];

/** Mobile bottom tabs of the user side. */
export const mobileTabs: NavItem[] = [
  { n: "01", to: "/", label: "nav.overview", icon: LayoutDashboard, exact: true },
  { n: "02", to: "/keys", label: "nav.keysShort", icon: KeyRound },
  { n: "03", to: "/usage", label: "nav.usage", icon: Gauge },
  { n: "00", to: "/me", label: "nav.me", icon: BookText },
];

/** Page code of a path, e.g. "OB-03". */
export function codeOf(path: string): string {
  const all = [...adminNav, ...userNav];
  const hit = all.find((i) => (i.exact ? path === i.to : path === i.to || path.startsWith(i.to + "/")));
  return `OB-${hit?.n ?? "00"}`;
}
