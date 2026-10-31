import { useRef, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router";

import { api } from "../api/client.ts";
import type { FilterOptions, RunFilter, RunList } from "../api/types.ts";
import { useApi } from "../api/useApi.ts";
import { Empty, Loaded, Page } from "../components/Page.tsx";
import { RunsTable } from "../components/RunsTable.tsx";
import { useSlashFocus } from "../keyboard.ts";

/** The filter parameters, in the order the API documents them. */
export const FILTER_KEYS = ["skill", "agent", "model", "verdict", "phase", "publication", "trigger", "q"] as const;
type FilterKey = (typeof FILTER_KEYS)[number];

const SELECTS: [Exclude<FilterKey, "q">, string, keyof FilterOptions][] = [
  ["skill", "Skill", "skills"],
  ["agent", "Agent", "agents"],
  ["model", "Model", "models"],
  ["verdict", "Verdict", "verdicts"],
  ["phase", "Phase", "phases"],
  ["publication", "Publication", "publications"],
  ["trigger", "Trigger", "triggers"],
];

const SORTS: [RunFilter["sort"], string][] = [
  ["newest", "Newest first"],
  ["oldest", "Oldest first"],
  ["skill", "Skill"],
  ["lift", "Lift"],
];

/** readQuery takes the API query from the page URL: only the parameters that are set, in a fixed order. */
export function readQuery(params: URLSearchParams): Partial<RunFilter> & { page?: number } {
  const out: Partial<RunFilter> & { page?: number } = {};
  for (const k of FILTER_KEYS) {
    const v = params.get(k);
    if (v) out[k] = v;
  }
  const sort = params.get("sort");
  if (sort && SORTS.some(([s]) => s === sort)) out.sort = sort as RunFilter["sort"];
  const page = Number(params.get("page"));
  if (Number.isInteger(page) && page > 1) out.page = page;
  return out;
}

/** RunsPage is /runs: the runs table with filters, sort and pagination, all kept in the URL. */
export function RunsPage() {
  const [params, setParams] = useSearchParams();
  const query = readQuery(params);
  const key = JSON.stringify(query);
  const state = useApi((signal) => api.runs(query, signal), [key]);
  return (
    <Page title="Runs">
      <Loaded state={state}>{(list) => <RunsBody list={list} params={params} setParams={setParams} />}</Loaded>
    </Page>
  );
}

function RunsBody({
  list,
  params,
  setParams,
}: {
  list: RunList;
  params: URLSearchParams;
  setParams: (p: URLSearchParams) => void;
}) {
  const qRef = useRef<HTMLInputElement>(null);
  useSlashFocus(() => qRef.current);
  const update = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    next.delete("page");
    setParams(next);
  };
  const onSearch = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    update("q", qRef.current?.value.trim() ?? "");
  };
  const active = FILTER_KEYS.filter((k) => params.get(k));
  const p = list.pagination;
  const pageHref = (n: number) => {
    const next = new URLSearchParams(params);
    if (n > 1) next.set("page", String(n));
    else next.delete("page");
    const s = next.toString();
    return s ? `?${s}` : "?";
  };
  return (
    <>
      <form className="filters" role="search" aria-label="Filter runs" onSubmit={onSearch}>
        {SELECTS.map(([k, label, opt]) => (
          <label key={k} className="field">
            <span className="field-label">{label}</span>
            <select value={params.get(k) ?? ""} onChange={(e) => update(k, e.target.value)}>
              <option value="">All</option>
              {list.options[opt].map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
              {params.get(k) && !list.options[opt].includes(params.get(k) ?? "") ? <option value={params.get(k) ?? ""}>{params.get(k)}</option> : null}
            </select>
          </label>
        ))}
        <label className="field">
          <span className="field-label">Sort</span>
          <select value={params.get("sort") ?? "newest"} onChange={(e) => update("sort", e.target.value === "newest" ? "" : e.target.value)}>
            {SORTS.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <label className="field field-grow">
          <span className="field-label">
            Name contains <kbd>/</kbd>
          </span>
          <input ref={qRef} type="search" name="q" defaultValue={params.get("q") ?? ""} key={params.get("q") ?? ""} placeholder="part of the run name" />
        </label>
        <button type="submit" className="button">
          Apply
        </button>
        {active.length > 0 || params.get("sort") ? (
          <Link to="/runs" className="button button-quiet">
            Clear
          </Link>
        ) : null}
      </form>
      <p className="muted result-line" aria-live="polite">
        {p.total} runs{active.length ? `, filtered by ${active.join(", ")}` : ""}
        {p.pages > 1 ? `, page ${p.page} of ${p.pages}` : ""}
      </p>
      {list.items.length === 0 ? <Empty>No run matches these filters.</Empty> : <RunsTable runs={list.items} caption="Runs" />}
      {p.pages > 1 ? (
        <nav className="pager" aria-label="Pages">
          {p.page > 1 ? (
            <Link to={pageHref(p.page - 1)} rel="prev">
              Previous
            </Link>
          ) : (
            <span className="muted">Previous</span>
          )}
          <ol>
            {Array.from({ length: p.pages }, (_, i) => i + 1).map((n) => (
              <li key={n}>
                {n === p.page ? (
                  <span aria-current="page" className="pager-current">
                    {n}
                  </span>
                ) : (
                  <Link to={pageHref(n)} aria-label={`Page ${n}`}>
                    {n}
                  </Link>
                )}
              </li>
            ))}
          </ol>
          {p.page < p.pages ? (
            <Link to={pageHref(p.page + 1)} rel="next">
              Next
            </Link>
          ) : (
            <span className="muted">Next</span>
          )}
        </nav>
      ) : null}
    </>
  );
}
