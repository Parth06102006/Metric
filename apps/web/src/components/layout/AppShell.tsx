import { Activity, ArrowUpRight, FolderKanban, House, LogOut, ScrollText } from "lucide-react";
import { Link, NavLink, Outlet, useMatch } from "react-router-dom";

import { useMe } from "../../features/identity/api";
import { cn } from "../../lib/cn";

export function AppShell() {
  const me = useMe();
  const isAdmin = me.data?.admin === true;
  const inProject = useMatch("/p/:projectId") !== null;
  const inDashboard = useMatch("/dashboard") !== null;
  const projectsCurrent = inProject ? "location" : inDashboard ? "page" : undefined;

  return (
    <div className="app-shell">
      <aside className="app-sidebar fixed inset-y-0 left-0 z-20 hidden w-60 overflow-y-auto border-r lg:flex lg:flex-col">
        <Link to="/dashboard" className="flex h-[76px] items-center gap-3 border-b border-white/10 px-6 text-base font-semibold tracking-tight">
          <span className="grid size-9 place-items-center rounded-xl bg-accent text-[#102118]"><Activity className="size-4" aria-hidden /></span>
          Metric
        </Link>
        <nav aria-label="Workspace navigation" className="px-4 pt-7">
          <p className="px-3 pb-3 text-[10px] font-semibold uppercase tracking-[.18em] text-muted/70">Workspace</p>
          <Link to="/dashboard" aria-current={projectsCurrent} className={cn("flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm transition-colors",projectsCurrent ? "bg-[#a5e5b614] text-accent" : "text-muted hover:bg-white/5 hover:text-ink")}><FolderKanban className="size-4" aria-hidden /> Projects</Link>
          {isAdmin && <NavLink to="/audit" className={({isActive}) => cn("mt-1 flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm transition-colors",isActive ? "bg-[#a5e5b614] text-accent" : "text-muted hover:bg-white/5 hover:text-ink")}><ScrollText className="size-4" aria-hidden /> Audit log</NavLink>}
        </nav>
        <div className="mt-auto border-t border-white/10 p-4">
          {me.data && <p className="mb-3 truncate px-2 text-xs text-muted" title={me.data.email}>{me.data.email}</p>}
          <a href="/oauth2/sign_out?rd=/" className="flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm text-muted hover:bg-white/5 hover:text-ink"><LogOut className="size-4" aria-hidden /> Sign out</a>
          <Link to="/home" className="mt-1 flex min-h-10 items-center gap-2 px-3 text-xs text-muted hover:text-ink">Product home <ArrowUpRight className="size-3.5" aria-hidden /></Link>
        </div>
      </aside>
      <header className="app-sidebar sticky top-0 z-10 flex h-14 items-center justify-between border-b px-4 lg:hidden">
        <Link to="/dashboard" className="flex items-center gap-2 font-semibold"><Activity className="size-4 text-accent" aria-hidden /> Metric</Link>
        <nav aria-label="Main navigation" className="flex items-center gap-1 text-xs">
          <Link to="/dashboard" aria-current={projectsCurrent} className={cn("rounded-md px-3 py-2",projectsCurrent ? "bg-white/10 text-ink":"text-muted")}>Projects</Link>
          {isAdmin && <NavLink to="/audit" className={({isActive})=>cn("rounded-md px-3 py-2",isActive?"bg-white/10 text-ink":"text-muted")}>Audit</NavLink>}
          <Link to="/home" aria-label="Product home" className="grid size-11 place-items-center rounded-md text-muted hover:text-ink"><House className="size-4" aria-hidden /></Link>
          <a href="/oauth2/sign_out?rd=/" aria-label="Sign out" className="grid size-11 place-items-center rounded-md text-muted hover:text-ink"><LogOut className="size-4" aria-hidden /></a>
        </nav>
      </header>
      <main className="app-main mx-auto px-4 py-7 sm:px-7 sm:py-9 lg:ml-60 lg:px-10 lg:py-10"><Outlet /></main>
    </div>
  );
}
