// The API client: typed GET requests to /api/v1/. Every request can be
// cancelled with an AbortSignal (pages cancel when they unmount).
import type {
  ApiErrorBody,
  Compare,
  ModelList,
  Overview,
  RunDetail,
  RunFilter,
  RunList,
  Site,
  SkillDetail,
  SkillList,
  TrialSession,
} from "./types.ts";

export const API_PREFIX = "/api/v1";

/** ApiError is a non-2xx answer, with the API's error code when the body had one. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

function isErrorBody(v: unknown): v is ApiErrorBody {
  return typeof v === "object" && v !== null && "error" in v && typeof (v as ApiErrorBody).error?.message === "string";
}

/** query builds "?a=1&b=2" from the defined, non-empty values. */
export function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

/** get fetches one API path (without the /api/v1 prefix) and parses the JSON body. */
export async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(API_PREFIX + path, { headers: { Accept: "application/json" }, signal });
  const text = await res.text();
  let body: unknown;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    throw new ApiError(res.status, "internal", `The server answered ${res.status} with a body that is not JSON.`);
  }
  if (!res.ok) {
    if (isErrorBody(body)) throw new ApiError(res.status, body.error.code, body.error.message);
    throw new ApiError(res.status, "internal", `The server answered ${res.status}.`);
  }
  return body as T;
}

const enc = encodeURIComponent;

/** api has one function per endpoint of CONTRACT.md, "Reporting site". */
export const api = {
  site: (signal?: AbortSignal) => get<Site>("/site", signal),
  overview: (signal?: AbortSignal) => get<Overview>("/overview", signal),
  runs: (filter: Partial<RunFilter> & { page?: number; pageSize?: number } = {}, signal?: AbortSignal) =>
    get<RunList>("/runs" + query(filter), signal),
  run: (name: string, job?: string, signal?: AbortSignal) => get<RunDetail>(`/runs/${enc(name)}` + query({ job }), signal),
  trial: (name: string, trial: string, job?: string, signal?: AbortSignal) =>
    get<TrialSession>(`/runs/${enc(name)}/trials/${enc(trial)}` + query({ job }), signal),
  compare: (
    name: string,
    caseId: string,
    opts: { attempt?: number; with?: string; without?: string; job?: string } = {},
    signal?: AbortSignal,
  ) => get<Compare>(`/runs/${enc(name)}/cases/${enc(caseId)}/compare` + query(opts), signal),
  skills: (signal?: AbortSignal) => get<SkillList>("/skills", signal),
  skill: (skill: string, signal?: AbortSignal) => get<SkillDetail>(`/skills/${enc(skill)}`, signal),
  models: (signal?: AbortSignal) => get<ModelList>("/models", signal),
};

/** trajectoryHref is the redacted ATIF download of a trial (a full page load, not an app route). */
export function trajectoryHref(name: string, trial: string, job?: string): string {
  return `${API_PREFIX}/runs/${enc(name)}/trials/${enc(trial)}/trajectory.json` + query({ job });
}
