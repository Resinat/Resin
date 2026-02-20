import type { HTMLAttributes } from "react";
import { cn } from "../../lib/cn";

type BadgeVariant = "neutral" | "success" | "warning" | "danger";

const variantClass: Record<BadgeVariant, string> = {
  neutral: "badge-neutral",
  success: "badge-success",
  warning: "badge-warning",
  danger: "badge-danger",
};

type BadgeProps = HTMLAttributes<HTMLSpanElement> & {
  variant?: BadgeVariant;
};

export function Badge({ className, variant = "neutral", ...props }: BadgeProps) {
  return <span className={cn("badge", variantClass[variant], className)} {...props} />;
}
