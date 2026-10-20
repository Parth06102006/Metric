import { Link, Route, Routes } from "react-router-dom";

import { AppShell } from "./components/layout/AppShell";
import { Dashboard } from "./routes/Dashboard";
import { ProjectDetail } from "./routes/ProjectDetail";

export function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<Dashboard />} />
        <Route path="p/:projectId" element={<ProjectDetail />} />
      </Route>
      <Route path="*" element={<NotFound />} />
    </Routes>
  );
}

function NotFound() {
  return (
    <div className="grid min-h-dvh place-items-center px-6">
      <div className="text-center">
        <p className="font-mono text-xs uppercase tracking-[0.2em] text-muted">404</p>
        <h1 className="mt-3 text-2xl font-semibold tracking-tight">Page not found</h1>
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
