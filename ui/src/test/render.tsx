// renderAt renders the app at a path with an in-memory router, for page tests.
import { render } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { vi } from "vitest";

import { fixtureFor } from "../api/fixtures/index.ts";
import { routes } from "../routes.tsx";

export function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  const view = render(<RouterProvider router={router} />);
  return { router, ...view };
}

/** stubApi answers one API path (without /api/v1, query ignored) with a changed copy of its fixture. */
export function stubApi(path: string, change: (body: unknown) => unknown): void {
  vi.stubGlobal("fetch", (input: RequestInfo | URL) => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, "http://test.invalid");
    const rest = url.pathname.replace(/^\/api\/v1/, "");
    const { status, body } = fixtureFor(rest + url.search);
    const out = rest === path ? change(structuredClone(body)) : body;
    return Promise.resolve(new Response(JSON.stringify(out), { status, headers: { "Content-Type": "application/json" } }));
  });
}
