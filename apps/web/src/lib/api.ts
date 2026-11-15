import type { z } from "zod";

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export function redirectToLogin(): void {
  const returnTo = encodeURIComponent(window.location.pathname + window.location.search);
  window.location.assign(`/oauth2/start?rd=${returnTo}`);
}

async function perform(path: string, init?: RequestInit): Promise<Response> {
  const headers = new Headers(init?.headers);
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(path, { ...init, headers });

  if (response.status === 401) {
    redirectToLogin();
    throw new ApiError(401, "Sign in required");
  }

  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    try {
      const body: unknown = await response.json();
      if (body && typeof body === "object" && "error" in body && typeof body.error === "string") {
        message = body.error;
      }
    } catch {
      // Non-JSON error body; keep the status message.
    }
    throw new ApiError(response.status, message);
  }

  return response;
}

async function request<T>(path: string, schema: z.ZodType<T>, init?: RequestInit): Promise<T> {
  const response = await perform(path, init);
  return schema.parse(await response.json());
}

/** For endpoints that answer with 204 and no body. */
async function requestNoContent(path: string, init?: RequestInit): Promise<void> {
  await perform(path, init);
}

export { request, requestNoContent };
