import * as React from "react";
import { Switch as S, Checkbox as C } from "radix-ui";
import { Check } from "lucide-react";
import { cn } from "@/lib/utils";

/** Square switch: off = outlined, on = yellow track with ink knob. */
export function Switch({ className, ...props }: React.ComponentProps<typeof S.Root>) {
  return (
    <S.Root
      className={cn(
        "group relative inline-flex h-6 w-11 shrink-0 items-center border-2 border-ink bg-bg disabled:opacity-50 data-[state=checked]:bg-yellow",
        className,
      )}
      {...props}
    >
      <S.Thumb className="block size-3.5 translate-x-[3px] bg-ink transition-transform data-[state=checked]:translate-x-[22px] data-[state=checked]:bg-on-yellow" />
    </S.Root>
  );
}

export function Checkbox({ className, ...props }: React.ComponentProps<typeof C.Root>) {
  return (
    <C.Root
      className={cn("inline-flex size-5 shrink-0 items-center justify-center border-2 border-ink bg-bg data-[state=checked]:bg-yellow", className)}
      {...props}
    >
      <C.Indicator>
        <Check className="size-3.5 text-on-yellow" strokeWidth={3} />
      </C.Indicator>
    </C.Root>
  );
}
