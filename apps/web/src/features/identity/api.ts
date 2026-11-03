import { useQuery } from "@tanstack/react-query";

import { zGetMeResponse } from "../../generated/api/zod.gen";
import { request } from "../../lib/api";

export function getMe() {
  return request("/api/auth/me", zGetMeResponse);
}

// The public homepage probes identity without redirecting anonymous visitors.
export async function getOptionalMe() {
  const response = await fetch("/api/auth/me", { credentials: "same-origin" });
  if (response.status === 401 || response.status === 403) return null;
  if (!response.ok) throw new Error(`Could not check sign-in (${response.status})`);
  return zGetMeResponse.parse(await response.json());
}

/**
 * The caller's identity. Shared through the query cache so the shell, the
 * dashboard, and the project page all read one request.
 */
export function useMe() {
  return useQuery({
    queryKey: ["me"],
    queryFn: getMe,
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
}
