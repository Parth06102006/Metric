import { useInfiniteQuery } from "@tanstack/react-query";
import { ScrollText } from "lucide-react";
import { useState } from "react";

import { Badge, Button, Card, ErrorNote, LoadingRows, Skeleton } from "../components/ui";
import {
  AUDIT_ACTIONS,
  AUDIT_PAGE_SIZE,
  auditActionLabel,
  listAudit,
  type AuditFilters,
} from "../features/audit/api";
import { useMe } from "../features/identity/api";
import { formatRelative, formatTimestamp } from "../lib/format";
import type { AuditEvent } from "../types/api";

type AuditCursor = { ts: string; id: number };

export function AuditLog() {
  const me = useMe();
  const isAdmin = me.data?.admin === true;
  const [action, setAction] = useState("");

  const audit = useInfiniteQuery({
    queryKey: ["audit", action],
    queryFn: ({ pageParam }: { pageParam: AuditCursor | null }) =>
      listAudit({
        action: action || undefined,
        before: pageParam?.ts,
        beforeId: pageParam?.id,
      } satisfies AuditFilters),
    initialPageParam: null as AuditCursor | null,
    getNextPageParam: (lastPage) => {
      if (lastPage.length < AUDIT_PAGE_SIZE) return null;
      const oldest = lastPage[lastPage.length - 1];
      return oldest ? { ts: oldest.ts, id: oldest.id } : null;
    },
    enabled: isAdmin,
  });

  const events = audit.data?.pages.flat() ?? [];

  return (
    <div className="space-y-8">
      <section className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Audit log</h1>
          <p className="mt-1 max-w-xl text-sm text-muted">
            Security events, newest first. Entries never contain keys or request bodies.
          </p>
        </div>

        {isAdmin && (
          <div className="w-full sm:w-64">
            <label htmlFor="audit-action" className="text-xs font-medium text-muted">
              Action
            </label>
            <select
              id="audit-action"
              value={action}
              onChange={(event) => setAction(event.target.value)}
              className="mt-1 min-h-11 w-full rounded-md border border-line bg-canvas px-3 py-2 text-sm text-ink focus:border-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            >
              <option value="">All actions</option>
              {AUDIT_ACTIONS.map((entry) => (
                <option key={entry.value} value={entry.value}>
                  {entry.label}
                </option>
              ))}
            </select>
          </div>
        )}
      </section>

      {me.isLoading && <div role="status" aria-label="Checking access" className="space-y-3"><span className="sr-only">Checking access</span><Skeleton className="h-10 w-48" /><Skeleton className="h-40 rounded-xl" /></div>}

      {!me.isLoading && !isAdmin && (
        <Card className="border-dashed p-8 text-center">
          <ScrollText className="mx-auto size-5 text-muted" aria-hidden />
          <p className="mt-3 text-sm font-medium">Administrators only</p>
          <p className="mx-auto mt-1 max-w-md text-sm text-muted">
            Ask an administrator if you need to review these events.
          </p>
        </Card>
      )}

      {isAdmin && audit.isLoading && <LoadingRows label="Loading audit events" rows={5} />}

      {isAdmin && audit.isError && (
        <ErrorNote
          message={audit.error instanceof Error ? audit.error.message : "Could not load audit events"}
        />
      )}

      {isAdmin && audit.isSuccess && events.length === 0 && (
        <Card className="border-dashed p-8 text-center">
          <p className="text-sm font-medium">No matching events</p>
          <p className="mx-auto mt-1 max-w-md text-sm text-muted">
            {action
              ? "Nothing has been recorded for this filter yet."
              : "Actions appear here as they happen."}
          </p>
        </Card>
      )}

      {isAdmin && events.length > 0 && (
        <Card className="overflow-hidden">
          <ul className="divide-y divide-line md:hidden">
            {events.map((event) => (
              <AuditMobileRow key={event.id} event={event} />
            ))}
          </ul>

          <div className="hidden overflow-x-auto md:block">
            <table className="w-full text-left text-sm">
              <caption className="sr-only">Audit events, newest first</caption>
              <thead className="border-b border-line text-[11px] uppercase tracking-[0.14em] text-muted">
                <tr>
                  <th scope="col" className="px-4 py-3 font-medium">
                    Time
                  </th>
                  <th scope="col" className="px-4 py-3 font-medium">
                    Actor
                  </th>
                  <th scope="col" className="px-4 py-3 font-medium">
                    Action
                  </th>
                  <th scope="col" className="px-4 py-3 font-medium">
                    Outcome
                  </th>
                  <th scope="col" className="px-4 py-3 font-medium">
                    Project
                  </th>
                  <th scope="col" className="px-4 py-3 font-medium">
                    Details
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {events.map((event) => (
                  <AuditRow key={event.id} event={event} />
                ))}
              </tbody>
            </table>
          </div>

          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line px-4 py-3">
            <p className="text-xs text-muted">
              {events.length} {events.length === 1 ? "event" : "events"} loaded
            </p>
            {audit.hasNextPage && (
              <Button
                variant="ghost"
                className="px-3 py-1.5 text-xs"
                onClick={() => void audit.fetchNextPage()}
                disabled={audit.isFetchingNextPage}
                aria-busy={audit.isFetchingNextPage}
              >
                {audit.isFetchingNextPage ? "Loading…" : "Load older"}
              </Button>
            )}
          </div>
        </Card>
      )}

      {isAdmin && audit.isFetchNextPageError && (
        <ErrorNote
          message={
            audit.error instanceof Error ? audit.error.message : "Could not load older events"
          }
        />
      )}
    </div>
  );
}

function AuditMobileRow({ event }: { event: AuditEvent }) {
  const details = Object.entries(event.details);

  return (
    <li className="p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-sm font-medium">{auditActionLabel(event.action)}</p>
          <code className="mt-0.5 block truncate font-mono text-[11px] text-muted">
            {event.action}
          </code>
        </div>
        <Badge tone={event.outcome === "success" ? "ok" : "danger"}>{event.outcome}</Badge>
      </div>

      <p className="mt-3 text-xs text-muted">
        <time dateTime={event.ts} title={formatTimestamp(event.ts)}>
          {formatRelative(event.ts)}
        </time>
        <span aria-hidden> · </span>
        <span className="break-all">{event.actor_email || "anonymous"}</span>
      </p>

      {event.target_project_id && (
        <p className="mt-1 text-xs text-muted">
          Project <code className="break-all font-mono text-ink">{event.target_project_id}</code>
        </p>
      )}

      {details.length > 0 && (
        <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted">
          {details.map(([key, value]) => (
            <span key={key} className="break-all">
              <span className="text-ink">{key}</span>: {value}
            </span>
          ))}
        </p>
      )}

      {event.request_id && (
        <p className="mt-1 break-all font-mono text-[11px] text-muted">req {event.request_id}</p>
      )}
    </li>
  );
}

function AuditRow({ event }: { event: AuditEvent }) {
  const details = Object.entries(event.details);

  return (
    <tr className="align-top">
      <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-muted">
        <time dateTime={event.ts} title={formatTimestamp(event.ts)}>
          {formatRelative(event.ts)}
        </time>
      </td>
      <td className="max-w-48 truncate px-4 py-3 text-xs" title={event.actor_email || "anonymous"}>
        {event.actor_email || "anonymous"}
      </td>
      <td className="px-4 py-3">
        <span className="text-sm">{auditActionLabel(event.action)}</span>
        <code className="mt-0.5 block font-mono text-[11px] text-muted">{event.action}</code>
      </td>
      <td className="px-4 py-3">
        <Badge tone={event.outcome === "success" ? "ok" : "danger"}>{event.outcome}</Badge>
      </td>
      <td className="px-4 py-3">
        <code className="block max-w-40 truncate font-mono text-xs text-muted">
          {event.target_project_id || "—"}
        </code>
      </td>
      <td className="px-4 py-3 text-xs text-muted">
        {details.length > 0 ? (
          <span className="flex flex-wrap gap-x-3 gap-y-1">
            {details.map(([key, value]) => (
              <span key={key} className="break-all">
                <span className="text-ink">{key}</span>: {value}
              </span>
            ))}
          </span>
        ) : (
          "—"
        )}
        {event.request_id && (
          <span className="mt-1 block font-mono text-[11px]" title={event.request_id}>
            req {event.request_id}
          </span>
        )}
      </td>
    </tr>
  );
}
