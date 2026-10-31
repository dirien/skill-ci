// The runs table, shared by the overview (recent runs) and /runs.
import { Link } from "react-router";

import type { RunSummary } from "../api/types.ts";
import { agentRef, duration, time } from "../format.ts";
import { Lift } from "./Lift.tsx";
import { PhasePill, PublicationPill, TierMarkers, TrialsText, VerdictPill } from "./Status.tsx";

export function runHref(name: string, job?: string): string {
  return `/runs/${encodeURIComponent(name)}${job ? `?job=${encodeURIComponent(job)}` : ""}`;
}

/** BreakAtSlash lets a long model name (openrouter/qwen/qwen3.8-flash) wrap after a slash only. */
function BreakAtSlash({ text }: { text: string }) {
  const parts = text.split("/");
  return (
    <>
      {parts.map((p, i) => (
        <span key={i}>
          <span className="nowrap">{i < parts.length - 1 ? `${p}/` : p}</span>
          {i < parts.length - 1 ? <wbr /> : null}
        </span>
      ))}
    </>
  );
}

/** TwoLineTime writes the date and the time as two unbreakable halves, so a narrow column wraps between them. */
function TwoLineTime({ iso }: { iso: string }) {
  const t = time(iso);
  const cut = t.indexOf(" ");
  if (cut < 0) return <>{t}</>;
  return (
    <>
      <span className="nowrap">{t.slice(0, cut)}</span> <span className="nowrap">{t.slice(cut + 1)}</span>
    </>
  );
}

export function RunsTable({ runs, caption }: { runs: RunSummary[]; caption: string }) {
  return (
    <div className="table-wrap">
      <table className="table">
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr>
            <th scope="col">Run</th>
            <th scope="col">Agent</th>
            <th scope="col">Model</th>
            <th scope="col">Status</th>
            <th scope="col">Verdict</th>
            <th scope="col">Lift</th>
            <th scope="col">Tiers</th>
            <th scope="col">Publication</th>
            <th scope="col">Trials</th>
            <th scope="col">Created</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <tr key={r.name}>
              <td>
                <Link to={runHref(r.name)} title={r.name} className="run-link">
                  <span className="strong">{r.skill}</span> {r.version}
                </Link>
                <div className="cell-sub">
                  {r.trigger}
                  {r.pushedBy ? ` by ${r.pushedBy}` : ""}
                  {r.retries > 0 ? (
                    <span className="retry" title={`Retried ${r.retries} time${r.retries === 1 ? "" : "s"}`}>
                      {" "}
                      · retried {r.retries}x
                    </span>
                  ) : null}
                  {r.required ? null : " · optional"}
                </div>
              </td>
              <td className="mono nowrap">
                {agentRef(r.agent, r.agentVersion)}
                {r.ranVersion && r.agentVersion && r.ranVersion !== r.agentVersion ? <div className="cell-sub">ran {r.ranVersion}</div> : null}
              </td>
              <td className="mono">
                <BreakAtSlash text={r.model} />
              </td>
              <td>
                <PhasePill phase={r.phase} />
                {r.durationSec !== undefined ? <div className="cell-sub">{duration(r.durationSec)}</div> : null}
              </td>
              <td>
                <VerdictPill verdict={r.verdict} />
              </td>
              <td>
                <Lift lift={r.lift} ci={r.liftCI} n={r.n} compact />
              </td>
              <td>
                <TierMarkers tiers={r.tiers} />
              </td>
              <td>
                <PublicationPill value={r.publication} />
              </td>
              <td>
                {r.trials === "stored" ? (
                  <Link to={`${runHref(r.name)}#trials`} aria-label={`Trial sessions of ${r.name}`} className="plain">
                    <TrialsText run={r} />
                  </Link>
                ) : (
                  <TrialsText run={r} />
                )}
              </td>
              <td>
                <TwoLineTime iso={r.createdAt} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
