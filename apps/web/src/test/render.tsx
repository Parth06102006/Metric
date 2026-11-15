import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

/** A fresh query cache and router for every isolated component test. */
export function renderWithProviders(
  component: ReactElement,
  { route = "/", path }: { route?: string; path?: string } = {},
) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });
  const user = userEvent.setup();
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[route]}>
        {path ? (
          <Routes>
            <Route path={path} element={component} />
            <Route path="/" element={<div>Dashboard destination</div>} />
            <Route path="/dashboard" element={<div>Dashboard destination</div>} />
          </Routes>
        ) : component}
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...view, client, user };
}
