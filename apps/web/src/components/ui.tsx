import type {
  ButtonHTMLAttributes,
  HTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
} from "react";

import { cn } from "../lib/cn";

type ButtonVariant = "primary" | "ghost" | "quiet";

export function Button({
  variant = "primary",
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  return (
    <button
      className={cn(
        "inline-flex min-h-11 items-center justify-center gap-2 rounded-md px-3.5 py-2 text-sm font-medium transition-colors disabled:pointer-events-none disabled:opacity-50",
        variant === "primary" && "bg-accent text-canvas hover:bg-accent-strong",
        variant === "ghost" && "border border-line bg-transparent text-ink hover:bg-raised",
        variant === "quiet" && "text-muted hover:text-ink",
        className,
      )}
      {...props}
    />
  );
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={cn(
        "min-h-11 w-full rounded-md border border-line bg-canvas px-3 py-2 text-sm text-ink placeholder:text-muted/70 focus:border-accent focus:outline-none",
        className,
      )}
      {...props}
    />
  );
}

export function Card({ className, children, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("rounded-lg border border-line bg-surface", className)} {...props}>
      {children}
    </div>
  );
}

export function Badge({
  tone = "neutral",
  children,
}: {
  tone?: "neutral" | "ok" | "warn" | "danger";
  children: ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] font-medium uppercase tracking-wide",
        tone === "neutral" && "border-line text-muted",
        tone === "ok" && "border-ok/40 bg-ok/10 text-ok",
        tone === "warn" && "border-warn/40 bg-warn/10 text-warn",
        tone === "danger" && "border-danger/40 bg-danger/10 text-danger",
      )}
    >
      {children}
    </span>
  );
}

export function Stat({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div className="rounded-lg border border-line bg-surface p-4">
      <p className="text-[11px] font-medium uppercase tracking-[0.14em] text-muted">{label}</p>
      <p className="mt-2 font-mono text-2xl font-medium tabular tracking-tight">{value}</p>
      {hint && <p className="mt-1 text-xs text-muted">{hint}</p>}
    </div>
  );
}

export function Spinner({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-3 text-sm text-muted" role="status">
      <span className="size-4 animate-spin rounded-full border-2 border-line border-t-accent" />
      {label}
    </div>
  );
}

export function ErrorNote({ message }: { message: string }) {
  return (
    <p role="alert" className="rounded-md border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger">
      {message}
    </p>
  );
}
