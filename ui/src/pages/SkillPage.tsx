import { Link, useParams } from "react-router";

import { api } from "../api/client.ts";
import type { Decision, MatrixCell, ModelColumn, SkillDetail } from "../api/types.ts";
import { useApi } from "../api/useApi.ts";
import { Loaded, Notice, Page, Section } from "../components/Page.tsx";
import { Lift } from "../components/Lift.tsx";
import { runHref } from "../components/RunsTable.tsx";
import { PhasePill, Pill, TierMarkers, VerdictPill, type Tone } from "../components/Status.tsx";
import { agentRef, score } from "../format.ts";

/** SkillPage is /skills/{skill}: versions x entries matrix, channel tags and policy decisions. */
export function SkillPage() {
  const { skill = "" } = useParams();
  const state = useApi((signal) => api.skill(skill, signal), [skill]);
  return (
    <Page title={skill}>
      <Loaded state={state}>{(d) => <SkillBody d={d} />}</Loaded>
    </Page>
  );
}

const DECISION: Record<Decision["kind"], [Tone, string]> = {
  pass: ["ok", "✓"],
  neutral: ["neutral", "●"],
  regressed: ["bad", "▼"],
  failed: ["bad", "✕"],
};

function ColumnHead({ c }: { c: ModelColumn }) {
  return (
    <th scope="col" className="matrix-col">
      <span className="mono">{agentRef(c.agent, c.agentVersion)}</span>
      <span className="mono cell-sub">{c.model}</span>
      <span className="cell-sub">{c.required ? "required" : "optional"}</span>
    </th>
  );
}

function Cell({ cell }: { cell: MatrixCell | null }) {
  if (!cell) return <td className="matrix-cell matrix-empty">not run</td>;
  return (
    <td className="matrix-cell">
      <div className="matrix-top">
        {cell.verdict ? <VerdictPill verdict={cell.verdict} /> : <PhasePill phase={cell.phase} />}
        <Link to={runHref(cell.run)} title={cell.run} aria-label={`Run ${cell.run}`}>
          run
        </Link>
      </div>
      {cell.withSkill !== undefined ? (
        <div className="matrix-scores">
          <span>
            with <span className="num">{score(cell.withSkill)}</span>
          </span>
          <span>
            without <span className="num">{score(cell.withoutSkill)}</span>
          </span>
        </div>
      ) : null}
      {cell.lift !== undefined ? <Lift lift={cell.lift} ci={cell.liftCI} n={cell.n} compact /> : null}
      <TierMarkers tiers={cell.tiers} />
    </td>
  );
}

function SkillBody({ d }: { d: SkillDetail }) {
  return (
    <>
      <dl className="counts counts-small">
        <div className="count">
          <dt>Stable</dt>
          <dd className="mono">{d.stable ?? "-"}</dd>
        </div>
        <div className="count">
          <dt>Beta</dt>
          <dd className="mono">{d.beta ?? "-"}</dd>
        </div>
        <div className="count">
          <dt>Runs</dt>
          <dd>
            <Link to={`/runs?skill=${encodeURIComponent(d.skill)}`}>{d.runs}</Link>
          </dd>
        </div>
      </dl>
      {d.channelsError ? (
        <Notice tone="warn" icon="!">
          Harbor tags could not be read: {d.channelsError}
        </Notice>
      ) : null}
      <Section title="Versions by entry">
        <div className="table-wrap">
          <table className="table matrix">
            <caption className="sr-only">Versions of {d.skill} by model list entry, with the policy decision per version</caption>
            <thead>
              <tr>
                <th scope="col">Version</th>
                {d.columns.map((c) => (
                  <ColumnHead key={c.entry} c={c} />
                ))}
                <th scope="col">Decision</th>
              </tr>
            </thead>
            <tbody>
              {d.rows.map((r) => {
                const [tone, icon] = DECISION[r.decision.kind];
                return (
                  <tr key={r.digest}>
                    <th scope="row" className="matrix-version">
                      <span className="mono strong">{r.version}</span>
                      <span className="mono cell-sub" title={r.digest}>
                        {r.digest.replace("sha256:", "").slice(0, 12)}
                      </span>
                      {r.channels.map((c) => (
                        <span key={c} className="tag">
                          {c}
                        </span>
                      ))}
                    </th>
                    {r.cells.map((cell, i) => (
                      <Cell key={d.columns[i]?.entry ?? i} cell={cell} />
                    ))}
                    <td className="decision">
                      <Pill tone={tone} icon={icon}>
                        {r.decision.headline}
                      </Pill>
                      <p>{r.decision.detail}</p>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </Section>
      <HowToRead />
    </>
  );
}

/** HowToRead is the legend for lifts and intervals. */
export function HowToRead() {
  return (
    <details className="disclosure how-to-read">
      <summary>How to read this</summary>
      <p>
        Lift is the with-skill score minus the without-skill score. The interval is the range the true lift lies in at the stated confidence; a
        version is promoted only when the whole interval is above zero.
      </p>
      <p>
        On the small bar the hairline is zero, the band is the interval and the dot is the lift, on a scale from -1 to +1. Inconclusive is
        neutral: the numbers do not show a difference either way.
      </p>
    </details>
  );
}
