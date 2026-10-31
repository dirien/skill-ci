// Shared page parts: the page heading, and the loading and error states of an API call.
import { useEffect, type ReactNode } from "react";

import type { ApiError } from "../api/client.ts";
import type { ApiState } from "../api/useApi.ts";

export function Page({ title, children }: { title: string; children?: ReactNode }) {
  useEffect(() => {
    document.title = `${title} - skill-ci`;
  }, [title]);
  return (
    <section className="page" aria-labelledby="page-title">
      <h1 id="page-title">{title}</h1>
      {children}
    </section>
  );
}

export function ErrorView({ error }: { error: ApiError }) {
  return (
    <div className="notice notice-bad" role="alert">
      <strong>{error.status === 404 ? "Not found" : "Cannot load this page"}</strong> {error.message}
    </div>
  );
}

/** Loaded renders children with the data, or the loading or error state. */
export function Loaded<T>({ state, children }: { state: ApiState<T>; children: (data: T) => ReactNode }) {
  if (state.status === "loading") return <p className="muted" aria-live="polite">Loading...</p>;
  if (state.status === "error") return <ErrorView error={state.error} />;
  return <>{children(state.data)}</>;
}

/** Todo marks a page section another work package builds. */
export function Todo({ what }: { what: string }) {
  return <p className="muted todo">Not built yet: {what}.</p>;
}
