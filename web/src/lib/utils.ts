import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** Short display form of a UUID (the last 6 hex digits, upper case). */
export function shortId(id: string): string {
  return id.replace(/-/g, "").slice(-6).toUpperCase();
}
