import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Copy, FolderKanban, Search } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";

import { Button, Card, ErrorNote, Input, LoadingRows } from "../components/ui";
import { useMe } from "../features/identity/api";
import { createProject, listProjects } from "../features/projects/api";
import { copyToClipboard } from "../lib/clipboard";
import type { CreatedProject } from "../types/api";

export function Dashboard() {
  const queryClient = useQueryClient();
  const projects = useQuery({ queryKey: ["projects"], queryFn: listProjects });
  const me = useMe();
  const isAdmin = me.data?.admin === true;

  const [name, setName] = useState("");
  const [search, setSearch] = useState("");
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<CreatedProject | null>(null);
  const [copied, setCopied] = useState(false);

  const create = useMutation({
    mutationFn: createProject,
    onSuccess: (project) => {
      setCreated(project);
      setCopied(false);
      setName("");
      setCreating(false);
      void queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
  });

  const searchTerm = search.trim().toLocaleLowerCase();
  const visibleProjects = projects.data?.filter((project) =>
    [project.name, project.project_id, ...(isAdmin && project.owner_email ? [project.owner_email] : [])]
      .some((value) => value.toLocaleLowerCase().includes(searchTerm)),
  );

  const copyKey = async () => {
    if (!created) return;
    const ok = await copyToClipboard(created.api_key);
    setCopied(ok);
    if (ok) window.setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div className="space-y-7 sm:space-y-9">
      <section className="flex flex-wrap items-end justify-between gap-5 border-b border-line/70 pb-6 sm:pb-8">
        <div>
          <p className="mb-2 text-[11px] font-semibold uppercase tracking-[.18em] text-accent/80">Workspace overview</p>
          <h1 className="text-3xl font-semibold tracking-tight sm:text-[2.6rem]">Projects</h1>
          <p className="mt-2 text-sm text-muted">Monitor traffic and manage your connected services.</p>
        </div>

        {isAdmin && (creating ? (
          <form
            className="flex w-full items-end gap-2 sm:w-auto"
            onSubmit={(event) => {
              event.preventDefault();
              if (name.trim()) create.mutate(name);
            }}
          >
            <div className="min-w-0 flex-1 sm:w-64 sm:flex-none">
              <label htmlFor="project-name" className="sr-only">
                Project name
              </label>
              <Input
                id="project-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="New project name"
                autoComplete="off"
                autoFocus
              />
            </div>
            <Button type="submit" disabled={create.isPending || !name.trim()}>
              {create.isPending ? "Creating…" : "Create"}
            </Button>
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                setCreating(false);
                setName("");
              }}
            >
              Cancel
            </Button>
          </form>
        ) : (
          <Button onClick={() => setCreating(true)}>New project</Button>
        ))}
      </section>

      {create.isError && (
        <ErrorNote message={create.error instanceof Error ? create.error.message : "Could not create project"} />
      )}

      {created && (
        <Card className="border-accent/40 bg-accent/5 p-4" role="status">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <p className="text-sm font-medium">Project created: {created.name}</p>
              <p className="mt-1 text-xs text-muted">
                Copy the API key now. It is shown once and stored only as a hash.
              </p>
            </div>
            <Button variant="ghost" onClick={copyKey} className="px-2.5 py-1.5 text-xs">
              <Copy className="size-3.5" aria-hidden />
              {copied ? "Copied" : "Copy key"}
            </Button>
          </div>

          <code className="mt-3 block overflow-x-auto rounded-md border border-line bg-canvas px-3 py-2 font-mono text-xs">
            {created.api_key}
          </code>

          <p className="mt-3 text-xs text-muted">
            Ingest URL{" "}
            <code className="font-mono text-ink">POST /ingest/{created.project_id}</code>
          </p>
        </Card>
      )}

      {projects.isLoading && <LoadingRows label="Loading projects" rows={4} />}

      {projects.isError && (
        <ErrorNote
          message={projects.error instanceof Error ? projects.error.message : "Could not load projects"}
        />
      )}

      {projects.data?.length === 0 && (
        <Card className="border-dashed p-10 text-center sm:p-14">
          <span className="mx-auto grid size-12 place-items-center rounded-2xl border border-line bg-raised text-accent"><FolderKanban className="size-5" aria-hidden /></span>
          <p className="mt-4 text-sm font-medium">
            {me.data && !isAdmin ? "No projects assigned to you" : "No projects yet"}
          </p>
          <p className="mx-auto mt-1 max-w-md text-sm text-muted">
            {me.data && !isAdmin
              ? "Ask an administrator to assign a project to your account."
              : "Create a project to get an ingest URL and API key."}
          </p>
        </Card>
      )}

      {projects.data && projects.data.length > 0 && (
        <section aria-label="Project list" className="overflow-hidden rounded-xl border border-line/90 bg-surface/65">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line/80 px-4 py-3 sm:px-5">
            <p className="text-xs text-muted" aria-live="polite">{visibleProjects?.length} of {projects.data.length} {projects.data.length === 1 ? "project" : "projects"}</p>
            <div className="relative w-full sm:w-64"><Search aria-hidden className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted" /><label htmlFor="project-search" className="sr-only">Search projects</label><Input id="project-search" type="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search projects" className="pl-9" /></div>
          </div>
          {visibleProjects?.length === 0 && <p className="p-6 text-sm text-muted">No projects match your search.</p>}
          <div className="divide-y divide-line/70">
          {visibleProjects?.map((project) => (
            <Link
              key={project.project_id}
              to={`/p/${project.project_id}`}
            className="group grid min-h-[76px] grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-4 py-3 transition-colors duration-200 hover:bg-raised/70 sm:grid-cols-[minmax(0,1fr)_minmax(130px,.55fr)_auto] sm:px-5"
            >
              <div className="flex min-w-0 items-center gap-3">
                <span className="grid size-9 shrink-0 place-items-center rounded-lg border border-line bg-canvas text-accent"><FolderKanban className="size-4" aria-hidden /></span>
                <div className="min-w-0"><h2 className="truncate text-sm font-medium tracking-tight">{project.name}</h2>
                  <p className="mt-1 truncate font-mono text-[11px] text-muted">{project.project_id}</p></div>
              </div>
              <div className="hidden min-w-0 sm:block">
                {isAdmin && project.owner_email ? <p className="truncate text-xs text-muted" title={project.owner_email}>{project.owner_email}</p> : <p className="text-xs text-muted">Open overview</p>}
              </div>
                <ArrowUpRight
                  className="size-4 shrink-0 text-muted transition-transform group-hover:translate-x-0.5 group-hover:-translate-y-0.5 group-hover:text-accent"
                  aria-hidden
                />
            </Link>
          ))}
          </div>
        </section>
      )}
    </div>
  );
}
