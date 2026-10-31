// useApi loads one API response for a page and cancels it when the inputs
// change or the page unmounts.
import { useEffect, useState } from "react";

import { ApiError } from "./client.ts";

export type ApiState<T> =
  | { status: "loading" }
  | { status: "error"; error: ApiError }
  | { status: "ok"; data: T };

export function useApi<T>(load: (signal: AbortSignal) => Promise<T>, deps: readonly unknown[]): ApiState<T> {
  const [state, setState] = useState<ApiState<T>>({ status: "loading" });
  useEffect(() => {
    const ctrl = new AbortController();
    setState({ status: "loading" });
    load(ctrl.signal).then(
      (data) => {
        if (!ctrl.signal.aborted) setState({ status: "ok", data });
      },
      (err: unknown) => {
        if (ctrl.signal.aborted) return;
        const error = err instanceof ApiError ? err : new ApiError(0, "internal", err instanceof Error ? err.message : String(err));
        setState({ status: "error", error });
      },
    );
    return () => ctrl.abort();
    // The caller lists the inputs of load; load itself is recreated every render.
  }, deps);
  return state;
}
