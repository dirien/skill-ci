// Formatting helpers shared by every page: one way to write a score, a lift, a
// duration, a byte count or a time, so the app reads as one product.
import type { Arm, Interval } from "./api/types.ts";

/** cap starts a status word with a capital letter: "passed" becomes "Passed". */
export function cap(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

/** score writes a 0..1 score with two decimals, or a dash when unknown. */
export function score(v: number | undefined): string {
  return v === undefined ? "-" : v.toFixed(2);
}

/** signed writes a lift with its sign: +0.59, -0.12, 0.00. */
export function signed(v: number | undefined): string {
  if (v === undefined) return "-";
  const s = v.toFixed(2);
  return v > 0 ? `+${s}` : s === "-0.00" ? "0.00" : s;
}

/** interval writes "95% CI +0.41 to +0.73". */
export function interval(ci: Interval): string {
  return `${ci.level} CI ${signed(ci.lower)} to ${signed(ci.upper)}`;
}

/** count writes an integer with thousands separators. */
export function count(v: number | undefined): string {
  return v === undefined ? "-" : Math.round(v).toLocaleString("en-US");
}

/** compact writes 950, 12.9K, 1.3M. */
export function compact(v: number | undefined): string {
  if (v === undefined) return "-";
  const a = Math.abs(v);
  if (a >= 1e6) return `${(v / 1e6).toFixed(1)}M`;
  if (a >= 1e4) return `${(v / 1e3).toFixed(1)}K`;
  return count(v);
}

/** usd writes a cost: $0.044, $1.20; "est." when the source is an estimate. */
export function usd(v: number | undefined, estimated = false): string {
  if (v === undefined) return "-";
  const digits = v === 0 ? 2 : v < 0.01 ? 4 : v < 1 ? 3 : 2;
  return `$${v.toFixed(digits)}${estimated ? " est." : ""}`;
}

/** duration writes seconds as 6.0s, 4m 27s, 2h 03m. */
export function duration(sec: number | undefined): string {
  if (sec === undefined) return "-";
  if (sec < 60) return `${sec.toFixed(1)}s`;
  const s = Math.round(sec);
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, "0")}m`;
}

/** bytes writes 390 B, 62.2 KiB, 1.4 MiB. */
export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MiB`;
}

/** time writes an RFC 3339 time as "2026-10-30 14:21 UTC" (the controller and the cluster speak UTC). */
export function time(iso: string | undefined): string {
  if (!iso) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** clock writes the time of day, "14:21:03". */
export function clock(iso: string | undefined): string {
  if (!iso) return "-";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toISOString().slice(11, 19);
}

/** offset writes a log offset from the first step, "+3.3s", or "-0.5s" for an entry before it. */
export function offset(sec: number | undefined): string {
  if (sec === undefined) return "";
  const sign = sec < 0 ? "-" : "+";
  const abs = Math.abs(sec);
  return abs < 60 ? `${sign}${abs.toFixed(1)}s` : `${sign}${duration(abs)}`;
}

/** armLabel writes "with skill" or "without skill". */
export function armLabel(arm: Arm): string {
  return arm === "with-skill" ? "with skill" : "without skill";
}

/** plural writes "1 trial", "2 trials". */
export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** agentRef writes "claude-code@2.1.292", or the agent alone when the version is unknown. */
export function agentRef(agent: string, version?: string): string {
  return version ? `${agent}@${version}` : agent;
}
