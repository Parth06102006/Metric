import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Copy } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";

import { Button, Card, ErrorNote, Input, Spinner } from "../components/ui";
import { createProject, listProjects } from "../features/projects/api";
import type { CreatedProject } from "../types/api";

export function Dashboard() {
  const queryClient = useQueryClient();
  const projects = useQuery({ queryKey: ["projects"], queryFn: listProjects });

  const [name, setName] = useState("");
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

  const copyKey = async () => {
    if (!created) return;
    try {
      await navigator.clipboard.writeText(created.api_key);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false);
    }
  };

  return (
    <div className="space-y-8">
      <section className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Projects</h1>
          <p className="mt-1 text-sm text-muted">
            Live traffic for your projects.
          </p>
        </div>

        {creating ? (
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
        )}
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

      {projects.isLoading && <Spinner label="Loading projects" />}

      {projects.isError && (
        <ErrorNote
          message={projects.error instanceof Error ? projects.error.message : "Could not load projects"}
        />
      )}

      {projects.data?.length === 0 && (
        <Card className="border-dashed p-8 text-center">
          <p className="text-sm font-medium">No projects yet</p>
          <p className="mx-auto mt-1 max-w-md text-sm text-muted">
            Create a project to get an ingest URL and API key.
          </p>
        </Card>
      )}

      {projects.data && projects.data.length > 0 && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {projects.data.map((project) => (
            <Link
              key={project.project_id}
              to={`/p/${project.project_id}`}
              className="group rounded-lg border border-line bg-surface p-4 transition-colors hover:border-accent/50 hover:bg-raised"
            >
              <div className="flex items-start justify-between gap-3">
                <h2 className="text-sm font-medium">{project.name}</h2>
                <ArrowUpRight
                  className="size-4 shrink-0 text-muted transition-colors group-hover:text-accent"
                  aria-hidden
                />
              </div>
              <p className="mt-3 truncate font-mono text-[11px] text-muted">{project.project_id}</p>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
