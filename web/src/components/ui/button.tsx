import * as React from "react";
import { Slot } from "radix-ui";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap border-2 font-sans font-semibold select-none transition-colors disabled:opacity-50 [&_svg]:size-4",
  {
    variants: {
      variant: {
        primary: "border-ink bg-yellow text-on-yellow hover:bg-ink hover:text-bg",
        secondary: "border-ink bg-bg text-ink hover:bg-rule",
        danger: "border-red bg-red text-on-red hover:underline hover:underline-offset-4",
        ghost: "border-transparent bg-transparent text-ink hover:border-ink",
        ink: "border-ink bg-ink text-bg hover:underline hover:underline-offset-4",
      },
      size: {
        sm: "h-8 px-3 text-[13px]",
        md: "h-10 px-4 text-sm",
        icon: "size-9 p-0",
        "icon-sm": "size-8 p-0",
      },
    },
    defaultVariants: { variant: "secondary", size: "md" },
  },
);

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

export function Button({ className, variant, size, asChild = false, type, ...props }: ButtonProps) {
  const Comp = asChild ? Slot.Root : "button";
  return <Comp className={cn(buttonVariants({ variant, size }), className)} type={asChild ? undefined : (type ?? "button")} {...props} />;
}

export { buttonVariants };
