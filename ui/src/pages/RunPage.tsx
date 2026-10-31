import { Link, useParams, useSearchParams } from "react-router";

import { api } from "../api/client.ts";
import type { GridCase, Report, RunDetail, RunSummary, TrialCell, TrialGrid, TrialsState, WithheldTrial } from "../api/types.ts";
import { useApi } from "../api/useApi.ts";
import { DimensionTable, ScoreMeter } from "../components/Charts.tsx";
import { Lift } from "../components/Lift.tsx";
import { Empty, Loaded, MetaStrip, Notice, Page, Section } from "../components/Page.tsx";
import { runHref } from "../components/RunsTable.tsx";
import { Mark, PhasePill, Pill, PublicationPill, TierMarkers, VerdictPill } from "../components/Status.tsx";
import { agentRef, cap, compact, count, duration, plural, score, time, usd } from "../format.ts";

const enc = encodeURIComponent;

/** trialHref is a trial session page; job is kept when the page shows an earlier attempt. */
export function trialHref(run: string, trial: string, job?: string, tab = ""): string {
  return `/runs/${enc(run)}/trials/${enc(trial)}${tab ? `/${tab}` : ""}${job ? `?job=${enc(job)}` : ""}`;
}

/** compareHref is the compare page of one case. */
export function compareHref(run: string, caseId: string, attempt?: number, job?: string): string {
  const q = new URLSearchParams();
  if (attempt && attempt > 1) q.set("attempt", String(attempt));
  if (job) q.set("job", job);
  const s = q.toString();
  return `/runs/${enc(run)}/cases/${enc(caseId)}/compare${s ? `?${s}` : ""}`;
}

/** RunPage is /runs/{name}[?job=]: one SkillEval with its trial grid. */
export function RunPage() {
  const { name = "" } = useParams();
  const [params] = useSearchParams();
  const job = params.get("job") ?? undefined;
  const state = useApi((signal) => api.run(name, job, signal), [name, job]);
  if (state.status !== "ok")
    return (
      <Page title={name}>
        <Loaded state={state}>{() => null}</Loaded>
      </Page>
    );
  return <RunBody d={state.data} job={job} />;
}

function retriedText(r: RunSummary, message: string): string {
  const times = r.retries === 1 ? "once" : `${r.retries} times`;
  return `Retried ${times} after an infrastructure failure: ${message}`;
}

function RunBody({ d, job }: { d: RunDetail; job?: string }) {
  const r = d.run;
  const noScores = r.withSkill === undefined && r.tiers.tier3 === "skipped";
  const current = d.attempts.find((a) => a.current);
  const viewing = job ? d.attempts.find((a) => a.jobName === job) : current;
  const ranOther = r.ranVersion && d.specAgentVersion && r.ranVersion !== d.specAgentVersion;
  const active = r.phase === "Pending" || r.phase === "Running";
  return (
    <Page
      docTitle={r.name}
      title={
        <>
          {r.skill} <span className="title-version">{r.version}</span>
        </>
      }
      sub={
        <>
          <span className="mono">{r.model}</span> on <span className="mono">{agentRef(r.agent, r.agentVersion)}</span>
          <span className="mono muted"> · {r.name}</span>
        </>
      }
      aside={
        <>
          <PhasePill phase={r.phase} />
          {r.verdict ? <VerdictPill verdict={r.verdict} /> : null}
        </>
      }
    >
      {job && viewing && !viewing.current ? (
        <Notice tone="info" icon="i">
          You are looking at attempt {viewing.number} (Job <code>{viewing.jobName}</code>), not the current one.{" "}
          <Link to={runHref(r.name)}>Show the current attempt</Link>.
        </Notice>
      ) : null}
      {r.message ? (
        <Notice tone={r.phase === "Failed" ? "bad" : "neutral"} icon={r.phase === "Failed" ? "✕" : "i"}>
          {cap(r.message)}
        </Notice>
      ) : null}
      {d.retried && r.retries > 0 ? (
        <Notice tone="warn" icon="↻">
          {retriedText(r, d.retried)}
        </Notice>
      ) : null}
      {d.regressed ? (
        <Notice tone="bad" icon="▼">
          {d.regressed}
        </Notice>
      ) : null}
      {d.inconclusive ? (
        <Notice tone="neutral" icon="○">
          {d.inconclusive}
        </Notice>
      ) : null}

      <MetaStrip
        label="Run"
        items={[
          { label: "Entry", value: <span className="mono">{r.entry}</span> },
          {
            label: "Agent version",
            value: ranOther ? (
              <>
                trials ran <span className="mono">{r.ranVersion}</span>, the spec pins <span className="mono">{d.specAgentVersion}</span>
              </>
            ) : (
              <span className="mono">{r.ranVersion ?? r.agentVersion ?? d.specAgentVersion ?? "-"}</span>
            ),
          },
          { label: "Required", value: r.required ? "yes" : "no" },
          { label: "Trigger", value: r.trigger },
          r.pushedBy ? { label: "Pushed by", value: r.pushedBy } : null,
          { label: "Created", value: time(r.createdAt) },
          r.completedAt ? { label: "Completed", value: time(r.completedAt) } : null,
          r.durationSec !== undefined ? { label: "Duration", value: duration(r.durationSec) } : null,
          { label: "Tiers", value: <TierMarkers tiers={r.tiers} /> },
          { label: "Publication", value: <PublicationPill value={r.publication} /> },
          d.channels.length ? { label: "Channels", value: d.channels.join(", ") } : null,
          { label: "Digest", value: <span className="mono" title={r.digest}>{r.digest.replace("sha256:", "").slice(0, 12)}</span> },
        ]}
      />

      {d.attempts.length > 1 ? <Attempts d={d} job={job} /> : null}

      <Section title="Scores">
        {noScores ? (
          <p>Tier 3 not run: {r.tiers.tier2 === "failed" && r.tiers.tier1 !== "failed" ? "Tier 2" : "Tier 1"} rejected this version, so no live trials were spent.</p>
        ) : r.withSkill === undefined ? (
          <Empty>No scores yet.</Empty>
        ) : (
          <dl className="counts">
            <div className="count">
              <dt>With skill</dt>
              <dd>
                {score(r.withSkill)}
                {d.withSkillStdErr !== undefined ? <span className="count-sub">± {d.withSkillStdErr.toFixed(2)} SE</span> : null}
              </dd>
            </div>
            <div className="count">
              <dt>Without skill</dt>
              <dd>
                {score(r.withoutSkill)}
                {d.withoutSkillStdErr !== undefined ? <span className="count-sub">± {d.withoutSkillStdErr.toFixed(2)} SE</span> : null}
              </dd>
            </div>
            <div className="count count-wide">
              <dt>Lift</dt>
              <dd>
                <Lift lift={r.lift} ci={r.liftCI} n={r.n} />
              </dd>
            </div>
            <div className="count">
              <dt>Samples</dt>
              <dd className="count-text">
                with {d.samples.withSkill.map((v) => score(v)).join(", ") || "-"}
                <br />
                without {d.samples.withoutSkill.map((v) => score(v)).join(", ") || "-"}
              </dd>
            </div>
          </dl>
        )}
      </Section>

      <Section title="SkillEvaluator tiers" extra={<TierMarkers tiers={r.tiers} />}>
        {d.findings.length === 0 ? (
          <Empty>{active ? "The tiers run once the Job starts." : "No findings."}</Empty>
        ) : (
          <ul className="findings-list">
            {d.findings.map((f) => (
              <li key={f}>
                <span className="mono">{f}</span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <ReportView report={d.report} active={active} />

      <TrialsView grid={d.trials} run={r.name} job={job} />

      <Section title="Sandbox and network">
        <MetaStrip
          label="Sandbox"
          items={[
            {
              label: "Sandbox",
              value: d.sandbox.profile ? (
                <>
                  profile <code>{d.sandbox.profile}</code>
                  {d.sandbox.warmPool ? (
                    <>
                      {" "}
                      with warm pool <code>{d.sandbox.warmPool}</code>
                    </>
                  ) : null}
                  {d.sandbox.specProfile && d.sandbox.specProfile !== d.sandbox.profile ? <> (spec asks for {d.sandbox.specProfile})</> : null}
                </>
              ) : (
                "a bare Sandbox, no profile"
              ),
            },
            { label: "Network mode", value: d.network.mode ? <code>{d.network.mode}</code> : "-" },
            d.network.source ? { label: "Mode from", value: d.network.source } : null,
            {
              label: "Enforced",
              value: d.network.enforced ? (
                <>
                  <Mark ok okText="enforced" /> by CiliumNetworkPolicy <code>{d.network.policy}</code>
                </>
              ) : (
                <>
                  <Mark ok={false} badText="not enforced" /> {d.network.reason ?? (active ? "not yet, the policy comes with the Job" : "not enforced")}
                </>
              ),
              wide: true,
            },
            { label: "Declared", value: d.network.declared.length ? d.network.declared.join(", ") : "none" },
            { label: "Allowed hosts", value: d.network.hosts.length ? d.network.hosts.join(", ") : d.network.mode === "full" ? "any" : "none" },
          ]}
        />
        {d.network.message ? <p className="muted">NetworkPolicy: {d.network.message}</p> : null}
        {d.network.legacy ? <p className="muted">This run predates network modes; the trials ran with the old defaults.</p> : null}
      </Section>

      <Section title="Conditions">
        {d.conditions.length === 0 ? (
          <Empty>No conditions.</Empty>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <caption className="sr-only">Conditions</caption>
              <thead>
                <tr>
                  <th scope="col">Type</th>
                  <th scope="col">Status</th>
                  <th scope="col">Reason</th>
                  <th scope="col">Message</th>
                  <th scope="col">Changed</th>
                </tr>
              </thead>
              <tbody>
                {d.conditions.map((c) => (
                  <tr key={c.type}>
                    <th scope="row">{c.type}</th>
                    <td>{c.status}</td>
                    <td>{c.reason}</td>
                    <td className="wrap">{c.message}</td>
                    <td className="nowrap">{time(c.changedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>
    </Page>
  );
}

const ATTEMPT_SESSIONS: Record<TrialsState, string> = {
  stored: "sessions stored",
  withheld: "sessions withheld",
  none: "no sessions",
  unknown: "sessions unknown",
};

function Attempts({ d, job }: { d: RunDetail; job?: string }) {
  const selected = job ?? d.attempts.find((a) => a.current)?.jobName;
  return (
    <Section title="Attempts">
      <ol className="attempts">
        {d.attempts.map((a) => (
          <li key={a.jobName} className={a.jobName === selected ? "attempt attempt-selected" : "attempt"}>
            {a.jobName === selected ? (
              <span aria-current="page" className="strong">
                Attempt {a.number}
              </span>
            ) : (
              <Link to={a.current ? runHref(d.run.name) : runHref(d.run.name, a.jobName)}>Attempt {a.number}</Link>
            )}
            {a.current ? <span className="tag">current</span> : null}
            <span className="mono muted"> {a.jobName}</span>
            <span className="muted"> · {a.reportDigest ? "report stored" : "no report"}</span>
            <span className="muted"> · {ATTEMPT_SESSIONS[a.trials]}</span>
            {a.message ? <div className="cell-sub">{a.message}</div> : null}
          </li>
        ))}
      </ol>
    </Section>
  );
}

function ReportFiles({ report }: { report: Report }) {
  if (report.files.length === 0) return null;
  return (
    <ul className="inline-list file-links">
      {report.files.map((f) => (
        <li key={f.name}>
          {/* A server page outside the app: a plain link is a full page load. */}
          <a href={f.href}>{f.name}</a>
        </li>
      ))}
    </ul>
  );
}

function ReportView({ report, active }: { report: Report; active: boolean }) {
  const extra = <ReportFiles report={report} />;
  switch (report.state) {
    case "none":
      return (
        <Section title="Report">
          <p className="muted">{active ? "The report is stored when the run finishes." : "No report stored for this run."}</p>
        </Section>
      );
    case "missing":
      return (
        <Section title="Report">
          <Notice tone="warn" icon="!">
            The report is no longer in the registry.
          </Notice>
        </Section>
      );
    case "error":
      return (
        <Section title="Report">
          <Notice tone="bad" icon="✕">
            The report could not be read from Harbor: {report.error ?? "unknown error"}
          </Notice>
        </Section>
      );
    case "files":
      return (
        <Section title="Report" extra={extra}>
          <p className="muted">The report files are stored; this run has no dimension scores.</p>
        </Section>
      );
    default:
      return (
        <Section title="Report" extra={extra}>
          {report.activation ? (
            <p className="muted">
              Skill activation: {report.activation.status}, {report.activation.activated} of {report.activation.positive} positive cases.
            </p>
          ) : null}
          {report.dimensions.length ? <DimensionTable dims={report.dimensions} caption="SkillEvaluator dimensions, mean per arm" /> : null}
        </Section>
      );
  }
}

const WITHHELD_REASON: Record<WithheldTrial["reason"], string> = {
  "secret-scan": "the secret scan found a value the redaction missed",
  size: "the trial is larger than the bundle limit",
  limit: "the bundle reached its size limit",
  unreadable: "the trial files could not be read",
};

function TrialCellView({ cell, grid, run, job }: { cell: TrialCell | null; grid: TrialGrid; run: string; job?: string }) {
  if (!cell) return <td className="trial-cell trial-missing">Missing</td>;
  const linked = grid.state === "stored" && cell.hasSession;
  if (cell.status === "withheld") {
    const w = grid.withheld.find((x) => x.id === cell.id);
    return (
      <td className="trial-cell">
        <Pill tone="warn" icon="!" title={w ? WITHHELD_REASON[w.reason] : undefined}>
          Withheld
        </Pill>
        <div className="cell-sub">{w ? `${w.reason}: ${WITHHELD_REASON[w.reason]}` : "not in the bundle"}</div>
      </td>
    );
  }
  const head =
    cell.status === "scored" ? (
      <span className="trial-score">
        <Mark ok={!!cell.passed} okText="passed" badText="did not pass" />
        <span className="num strong">{score(cell.score)}</span>
        {cell.score !== undefined ? <ScoreMeter value={cell.score} /> : null}
      </span>
    ) : (
      <Pill tone="bad" icon="✕">
        {cap(cell.status)}
        {cell.errorType ? ` ${cell.errorType}` : ""}
      </Pill>
    );
  return (
    <td className="trial-cell">
      {linked ? (
        <Link to={trialHref(run, cell.id, job)} className="trial-link" aria-label={`Session ${cell.id}, ${cell.arm}, attempt ${cell.attempt}`}>
          {head}
        </Link>
      ) : (
        head
      )}
      <div className="cell-sub">
        {cell.criteria ? `${cell.criteria.passed}/${cell.criteria.total} criteria` : null}
        {cell.skillInvoked !== undefined ? ` · skill ${cell.skillInvoked ? "used" : "not used"}` : null}
        {cell.dropped > 0 ? (
          <span className="tone-text-bad">
            {" "}
            · <span aria-hidden="true">✕</span> {cell.dropped} dropped
          </span>
        ) : null}
      </div>
      {cell.steps > 0 || cell.durationSec !== undefined ? (
        <div className="cell-sub">
          {cell.steps > 0 ? `${plural(cell.steps, "step")}, ${plural(cell.toolCalls, "tool call")}` : null}
          {cell.durationSec !== undefined ? ` · ${duration(cell.durationSec)}` : null}
          {cell.tokensIn !== undefined ? ` · ${compact(cell.tokensIn)} in` : null}
          {cell.costUsd !== undefined ? ` · ${usd(cell.costUsd, cell.costEstimated)}` : null}
        </div>
      ) : null}
      {cell.id && !linked && grid.state === "fallback" ? <div className="cell-sub mono">{cell.id}</div> : null}
    </td>
  );
}

function CaseBlock({ c, grid, run, job }: { c: GridCase; grid: TrialGrid; run: string; job?: string }) {
  const attempts = Array.from({ length: Math.max(grid.attemptsPerCase, c.withSkill.length, c.withoutSkill.length) }, (_, i) => i + 1);
  const canCompare = grid.state === "stored";
  return (
    <div className="case">
      <div className="case-head">
        <h3 className="mono">{c.id}</h3>
        {canCompare && attempts.length === 1 ? (
          <Link to={compareHref(run, c.id, 1, job)} className="button button-quiet">
            Compare with and without
          </Link>
        ) : null}
      </div>
      <p className="case-question clamp-2" title={c.question}>
        {c.question}
      </p>
      {c.expectedBehaviors.length ? (
        <details className="disclosure">
          <summary>{plural(c.expectedBehaviors.length, "expected behavior")}</summary>
          <ol className="behaviors-plain">
            {c.expectedBehaviors.map((b) => (
              <li key={b}>{b}</li>
            ))}
          </ol>
        </details>
      ) : null}
      <div className="table-wrap">
        <table className="table trial-grid">
          <caption className="sr-only">Trials of case {c.id}: arms by attempt</caption>
          <thead>
            <tr>
              <th scope="col">Arm</th>
              {attempts.map((a) => (
                <th scope="col" key={a}>
                  Attempt {a}
                  {canCompare && attempts.length > 1 ? (
                    <>
                      {" "}
                      <Link to={compareHref(run, c.id, a, job)} aria-label={`Compare attempt ${a} of ${c.id}`} className="small">
                        compare
                      </Link>
                    </>
                  ) : null}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {(
              [
                ["With skill", c.withSkill],
                ["Without skill", c.withoutSkill],
              ] as const
            ).map(([label, cells]) => (
              <tr key={label}>
                <th scope="row">{label}</th>
                {attempts.map((a) => (
                  <TrialCellView key={a} cell={cells[a - 1] ?? null} grid={grid} run={run} job={job} />
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function TrialsView({ grid, run, job }: { grid: TrialGrid; run: string; job?: string }) {
  const t = grid.totals;
  let notice = null;
  switch (grid.state) {
    case "fallback":
      notice = (
        <Notice tone="info" icon="i">
          {grid.reason ?? "No sessions: this run is older than trial bundles, so only the scores from result.json are shown."}
        </Notice>
      );
      break;
    case "withheld":
      notice = (
        <Notice tone="warn" icon="!">
          {grid.reason ?? "No sessions: the runner withheld the trial bundle."}
        </Notice>
      );
      break;
    case "missing":
      notice = <p className="muted">{grid.reason ?? "No sessions: the trial bundle is no longer in the registry."}</p>;
      break;
    case "error":
      notice = (
        <Notice tone="bad" icon="✕">
          The trial bundle could not be read from Harbor{grid.reason ? `: ${grid.reason}` : "."}
        </Notice>
      );
      break;
    case "none":
      notice = <p className="muted">{grid.reason ?? "This run has no trials."}</p>;
      break;
    default:
      break;
  }
  return (
    <Section title="Trials" id="trials">
      {notice}
      {grid.cases.map((c) => (
        <CaseBlock key={c.id} c={c} grid={grid} run={run} job={job} />
      ))}
      {t.trials > 0 ? (
        <MetaStrip
          label="Trial totals"
          items={[
            { label: "Trials", value: `${t.trials} (${t.scored} scored, ${t.unscored} unscored, ${t.withheld} withheld)` },
            { label: "Tokens in", value: count(t.tokensIn) },
            { label: "Tokens out", value: count(t.tokensOut) },
            { label: "Cached", value: count(t.tokensCached) },
            { label: "Cost", value: usd(t.costUsd, t.costEstimated) },
            { label: "Agent time", value: duration(t.agentSec) },
            { label: "Trial time", value: duration(t.durationSec) },
          ]}
        />
      ) : null}
      {grid.credentials.length ? (
        <MetaStrip
          label="Credential routes"
          items={grid.credentials.map((c) => ({
            label: `${c.role} credential`,
            value: (
              <>
                <code>{c.env}</code> via {c.host}
              </>
            ),
          }))}
        />
      ) : null}
      {grid.network ? (
        <p className="muted">
          Network flows: {grid.network.state}
          {grid.network.reason ? `; ${grid.network.reason}` : ""}
          {grid.network.state === "collected" || grid.network.state === "partial"
            ? `, ${grid.network.forwarded} forwarded, ${grid.network.dropped} dropped`
            : ""}
          .
        </p>
      ) : null}
      {grid.state === "stored" && grid.bundleDigest ? (
        <p className="muted small">
          Bundle <span className="mono" title={grid.bundleDigest}>{grid.bundleDigest.replace("sha256:", "").slice(0, 12)}</span>
          {grid.job ? (
            <>
              {" "}
              from Job <span className="mono">{grid.job}</span>
            </>
          ) : null}
        </p>
      ) : null}
    </Section>
  );
}
