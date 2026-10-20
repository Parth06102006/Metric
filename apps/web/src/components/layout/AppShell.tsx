import { useQuery } from "@tanstack/react-query";
import { Activity, LogOut } from "lucide-react";
import { Link, Outlet } from "react-router-dom";

import { getMe } from "../../features/projects/api";

export function AppShell() {
  const me = useQuery({
    queryKey: ["me"],
    queryFn: getMe,
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });

  return (
    <div className="min-h-dvh">
      <header className="sticky top-0 z-10 border-b border-line bg-canvas/85 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-6xl items-center justify-between gap-4 px-6">
          <Link to="/" className="flex items-center gap-2 text-sm font-semibold tracking-tight">
            <span className="grid size-6 place-items-center rounded bg-accent text-canvas">
              <Activity className="size-4" aria-hidden />
            </span>
            Metric
            <span className="hidden text-xs font-normal text-muted sm:inline">
              Traffic command center
            </span>
          </Link>

          <div className="flex items-center gap-4 text-xs text-muted">
            {me.data && (
              <span className="hidden max-w-56 truncate sm:inline" title={me.data.email}>
                {me.data.email}
              </span>
            )}
            <a
              href="/oauth2/sign_out?rd=/"
              className="inline-flex min-h-11 items-center gap-1.5 transition-colors hover:text-ink"
            >
              <LogOut className="size-3.5" aria-hidden />
              Sign out
            </a>
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-6xl px-6 py-8">
        <Outlet />
      </main>
    </div>
  );
}
