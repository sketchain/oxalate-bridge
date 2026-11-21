import * as React from "react";
import { Tabs as T } from "radix-ui";
import { cn } from "@/lib/utils";

export const Tabs = T.Root;
export const TabsContent = T.Content;

export function TabsList({ className, ...props }: React.ComponentProps<typeof T.List>) {
  return <T.List className={cn("scroll-x flex border-b-2 border-ink", className)} {...props} />;
}

export function TabsTrigger({ className, ...props }: React.ComponentProps<typeof T.Trigger>) {
  return (
    <T.Trigger
      className={cn(
        "-mb-[2px] inline-flex h-10 shrink-0 items-center gap-2 border-2 border-b-0 border-transparent px-4 text-sm font-semibold text-muted",
        "data-[state=active]:border-ink data-[state=active]:bg-yellow data-[state=active]:text-on-yellow hover:text-ink",
        className,
      )}
      {...props}
    />
  );
}
