import { Toaster as Sonner } from "sonner";

/** Toasts: square, 2px ink frame, no shadow; errors get a red bar. */
export function Toaster() {
  return (
    <Sonner
      position="bottom-right"
      offset={16}
      mobileOffset={{ bottom: 80, left: 16, right: 16 }}
      toastOptions={{
        unstyled: true,
        classNames: {
          toast: "flex w-full items-center gap-3 border-2 border-ink bg-bg px-4 py-3 text-sm text-ink sm:w-[356px]",
          title: "font-semibold",
          description: "text-muted text-[13px]",
          error: "border-l-[8px] border-l-red",
          success: "border-l-[8px] border-l-yellow",
          actionButton: "ml-auto border-2 border-ink bg-yellow px-2 py-1 text-on-yellow font-semibold",
        },
      }}
    />
  );
}
