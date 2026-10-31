import { Link, useParams, useSearchParams } from "react-router";

import { api } from "../api/client.ts";
import type { Compare, CompareSide, TrialCell } from "../api/types.ts";
import { useApi } from "../api/useApi.ts";
import { ArmLegend } from "../components/Charts.tsx";
import { Empty, Loaded, Notice, Page, PromptBox } from "../components/Page.tsx";
import { runHref } from "../components/RunsTable.tsx";
import { Mark, PassBadge } from "../components/Status.tsx";
import { compact, duration, score, usd } from "../format.ts";
import { EntryView } from "../session/Entry.tsx";
import { compareHref, trialHref } from "./RunPage.tsx";
import { FileView } from "./TrialPage.tsx";

/** ComparePage is /runs/{name}/cases/{case}/compare[?attempt=&with=&without=]: both arms side by side. */
export function ComparePage() {
  const { name = "", case: caseId = "" } = useParams();
  const [params] = useSearchParams();
  const attempt = Number(params.get("attempt")) || undefined;
  const withId = params.get("with") ?? undefined;
  const withoutId = params.get("without") ?? undefined;
  const job = params.get("job") ?? undefined;
  const state = useApi(
    (signal) => api.compare(name, caseId, { attempt, with: withId, without: withoutId, job }, signal),
    [name, caseId, attempt, withId, withoutId, job],
  );
  return (
    <Page title={`Compare ${caseId}`}>
      <Loaded state={state}>{(c) => <CompareBody c={c} job={job} />}</Loaded>
    </Page>
  );
}

type Row = [string, (t: TrialCell) => string];

const ROWS: Row[] = [
  ["Score", (t) => score(t.score)],
  ["Criteria", (t) => (t.criteria ? `${t.criteria.passed} of ${t.criteria.total}` : "-")],
  ["Skill used", (t) => (t.skillInvoked === undefined ? "-" : t.skillInvoked ? "yes" : "no")],
  ["Steps", (t) => String(t.steps)],
  ["Tool calls", (t) => String(t.toolCalls)],
  ["Tokens in", (t) => compact(t.tokensIn)],
  ["Tokens out", (t) => compact(t.tokensOut)],
  ["Cost", (t) => usd(t.costUsd, t.costEstimated)],
  ["Agent time", (t) => duration(t.agentSec)],
  ["Dropped connections", (t) => String(t.dropped)],
];

function CompareBody({ c, job }: { c: Compare; job?: string }) {
  const w = c.withSkill.trial;
  const wo = c.withoutSkill.trial;
  return (
    <>
      <p className="page-sub">
        <Link to={runHref(c.run.name, c.run.currentJob ? undefined : c.run.job)}>
          {c.run.skill} {c.run.version}
        </Link>{" "}
        · <span className="mono">{c.run.entry}</span> · attempt {c.attempt} of {c.attemptsPerCase}, paired by attempt
      </p>
      {c.attemptsPerCase > 1 ? (
        <nav aria-label="Attempts" className="attempt-nav">
          <span className="muted">Attempt</span>
          <ul className="inline-list">
            {Array.from({ length: c.attemptsPerCase }, (_, i) => i + 1).map((a) => (
              <li key={a}>
                {a === c.attempt ? (
                  <span aria-current="page" className="strong">
                    {a}
                  </span>
                ) : (
                  <Link to={compareHref(c.run.name, c.case.id, a, job)}>{a}</Link>
                )}
              </li>
            ))}
          </ul>
        </nav>
      ) : null}
      <PromptBox text={c.case.question} label="Case question" />
      {w || wo ? (
        <div className="table-wrap">
          <table className="table compare-table">
            <caption className="sr-only">The two trials side by side</caption>
            <thead>
              <tr>
                <th scope="col">
                  <ArmLegend />
                </th>
                <th scope="col">With skill</th>
                <th scope="col">Without skill</th>
              </tr>
            </thead>
            <tbody>
              {ROWS.map(([label, f]) => (
                <tr key={label}>
                  <th scope="row">{label}</th>
                  <td className="num">{w ? f(w) : "-"}</td>
                  <td className="num">{wo ? f(wo) : "-"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      <div className="compare">
        <Side title="With skill" side={c.withSkill} run={c.run.name} job={job} behaviors={c.case.expectedBehaviors} />
        <Side title="Without skill" side={c.withoutSkill} run={c.run.name} job={job} behaviors={c.case.expectedBehaviors} />
      </div>
    </>
  );
}

function Side({ title, side, run, job, behaviors }: { title: string; side: CompareSide; run: string; job?: string; behaviors: string[] }) {
  const t = side.trial;
  const id = title.toLowerCase().replace(/\s+/g, "-");
  const log = side.log.filter((e) => e.category !== "usage" && e.category !== "meta");
  return (
    <section className="compare-side panel" aria-labelledby={`${id}-h`}>
      <header className="panel-head">
        <div className="compare-side-title">
          <h2 id={`${id}-h`}>{title}</h2>
          {t?.status === "scored" ? <Mark ok={!!t.passed} okText="passed" badText="did not pass" /> : null}
          {t?.score !== undefined ? <span className="num strong">{score(t.score)}</span> : null}
        </div>
        {t ? (
          <>
            {t.hasSession ? (
              <Link to={trialHref(run, t.id, job)} className="small">
                Open session {t.id}
              </Link>
            ) : (
              <span className="mono small muted">{t.id}</span>
            )}
          </>
        ) : null}
      </header>
      {side.missing ? (
        <Notice tone="warn" icon="!">
          {side.missing}
        </Notice>
      ) : null}
      {side.criteria ? (
        <ol className="criteria-list">
          {side.criteria.behaviors.map((b) => (
            <li key={b.index} className="criterion">
              <PassBadge passed={b.passed} />
              <div>
                <div className="criterion-title">{b.text}</div>
                {b.reason ? (
                  <div className="criterion-reason clamp-2" title={b.reason}>
                    {b.reason}
                  </div>
                ) : null}
              </div>
            </li>
          ))}
        </ol>
      ) : side.missing ? null : (
        <ol className="behaviors-plain">
          {behaviors.map((b) => (
            <li key={b}>{b}</li>
          ))}
        </ol>
      )}
      {side.header?.final ? (
        <div className="prompt">
          <div className="prompt-label">Final answer</div>
          <pre className="prompt-text">{side.header.final}</pre>
        </div>
      ) : null}
      {log.length === 0 ? (
        side.missing ? null : (
          <>
            <h3 className="subhead">Log</h3>
            <Empty>This trial recorded no log entries.</Empty>
          </>
        )
      ) : (
        <>
          <h3 className="subhead">Log</h3>
          <ol className="log log-compact" aria-label={`${title} log`}>
            {log.map((e) => (
              <EntryView key={e.id} e={e} target={false} compact />
            ))}
          </ol>
        </>
      )}
      {side.files.length ? (
        <>
          <h3 className="subhead">Files</h3>
          {side.files.map((f, i) => (
            <FileView key={`${f.entryId}-${i}`} f={f} link={false} />
          ))}
        </>
      ) : null}
    </section>
  );
}
