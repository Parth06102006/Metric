import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense } from "react";
import { Link, Navigate, Route, Routes } from "react-router-dom";

import { AppShell } from "./components/layout/AppShell";
import { AuditLog } from "./routes/AuditLog";
import { Dashboard } from "./routes/Dashboard";
import { ProjectDetail } from "./routes/ProjectDetail";
import { getOptionalMe } from "./features/identity/api";

const Landing = lazy(() => import("./routes/Landing"));

function HomeEntry() {
  const identity = useQuery({ queryKey: ["entry-identity"], queryFn: getOptionalMe, retry: false });
  if (identity.isLoading) return <div role="status" aria-label="Checking sign-in" className="grid min-h-dvh place-items-center text-muted"><div className="w-64 space-y-3"><span className="sr-only">Checking sign-in</span><span className="skeleton block h-8 w-32 rounded-md"/><span className="skeleton block h-3 w-full rounded-md"/></div></div>;
  if (identity.isError) return <div className="grid min-h-dvh place-items-center"><div><p>Could not check sign-in.</p><button onClick={() => void identity.refetch()} className="mt-2 underline">Try again</button><Link className="ml-4 underline" to="/home">View home</Link></div></div>;
  return identity.data ? <Navigate to="/dashboard" replace /> : <Landing />;
}

export function App() {
  return (
    <Suspense fallback={<div role="status" aria-label="Loading page" className="grid min-h-dvh place-items-center"><div className="w-64 space-y-3"><span className="sr-only">Loading page</span><span className="skeleton block h-8 w-32 rounded-md"/><span className="skeleton block h-3 w-full rounded-md"/></div></div>}><Routes>
      <Route path="/" element={<HomeEntry />} />
      <Route path="/home" element={<Landing />} />
      <Route element={<AppShell />}>
        <Route path="dashboard" element={<Dashboard />} />
        <Route path="p/:projectId" element={<ProjectDetail />} />
        <Route path="audit" element={<AuditLog />} />
      </Route>
      <Route path="*" element={<NotFound />} />
    </Routes></Suspense>
  );
}

function NotFound() {
  return (
    <div className="grid min-h-dvh place-items-center bg-[#09120f] px-6">
      <div className="text-center">
        <p className="font-mono text-xs uppercase tracking-[0.2em] text-accent">404 / NOT FOUND</p>
        <h1 className="mt-3 text-3xl font-semibold tracking-tight">Page not found</h1>
        <p className="mt-2 text-sm text-muted">That route does not exist in this command center.</p>
        <Link
          to="/"
          className="mt-5 inline-flex rounded-md bg-accent px-3.5 py-2 text-sm font-medium text-canvas transition-colors hover:bg-accent-strong"
        >
          Back to projects
        </Link>
      </div>
    </div>
  );
}
