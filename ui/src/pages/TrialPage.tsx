import { Link, NavLink, Outlet, useOutletContext, useParams, useSearchParams } from "react-router";

import { api } from "../api/client.ts";
import type { FileChange, JudgeMetric, TrialMetadata, TrialNetwork, TrialSession } from "../api/types.ts";
import { useApi } from "../api/useApi.ts";
import { PhaseBar, ScoreMeter } from "../components/Charts.tsx";
import { Empty, Loaded, MetaStrip, Notice, Page, PromptBox, Section } from "../components/Page.tsx";
import { runHref } from "../components/RunsTable.tsx";
import { Mark, PassBadge, Pill } from "../components/Status.tsx";
import { agentRef, armLabel, clock, count, duration, plural, score, time, usd } from "../format.ts";
import { CriteriaPanel, FindingsPanel } from "../session/Aside.tsx";
import { EntryLink, FoldText, StepChips } from "../session/Entry.tsx";
import { LogView } from "../session/LogView.tsx";

/** TrialLayout is /runs/{name}/trials/{trial}[?job=]: header, metadata and the tabs. */
export function TrialLayout() {
  const { name = "", trial = "" } = useParams();
  const [params] = useSearchParams();
  const job = params.get("job") ?? undefined;
  const state = useApi((signal) => api.trial(name, trial, job, signal), [name, trial, job]);
  if (state.status !== "ok")
    return (
      <Page title={trial}>
        <Loaded state={state}>{() => null}</Loaded>
      </Page>
    );
  return <TrialBody s={state.data} job={job} />;
}

function SkillValue({ m }: { m: TrialMetadata }) {
  const s = m.skill;
  const word = { invoked: "invoked", loaded: "loaded, not invoked", "not-used": "not used", unknown: "unknown" }[s.state];
  const tone = s.state === "invoked" ? "ok" : s.state === "loaded" ? "warn" : "neutral";
  const icon = s.state === "invoked" ? "✓" : s.state === "loaded" ? "!" : "○";
  return (
    <span title={s.reason}>
      <Pill tone={tone} icon={icon}>
        {word}
      </Pill>{" "}
      {s.matched ?? s.expected ? <span className="mono">{s.matched ?? s.expected}</span> : null}
    </span>
  );
}

function metadataItems(s: TrialSession) {
  const m = s.metadata;
  return [
    { label: "Arm", value: armLabel(m.arm) },
    { label: "Case", value: <span className="mono">{m.case}</span> },
    { label: "Attempt", value: `${m.attempt} of ${m.attemptsPerCase}` },
    { label: "Agent", value: <span className="mono">{agentRef(m.agent, m.agentVersion)}</span> },
    { label: "Model", value: <span className="mono">{m.model}</span> },
    m.provider ? { label: "Provider", value: m.provider } : null,
    m.workspace ? { label: "Workspace", value: <span className="mono">{m.workspace}</span> } : null,
    { label: "Skill", value: <SkillValue m={m} /> },
    m.credentials.length
      ? {
          label: "Credentials",
          wide: true,
          value: (
            <ul className="inline-list">
              {m.credentials.map((c) => (
                <li key={`${c.role}-${c.env}`}>
                  {c.role} <code>{c.env}</code> via {c.host}
                </li>
              ))}
            </ul>
          ),
        }
      : null,
    m.startedAt ? { label: "Started", value: time(m.startedAt) } : null,
    m.durationSec !== undefined ? { label: "Duration", value: duration(m.durationSec) } : null,
    m.tokens.in !== undefined
      ? {
          label: "Tokens",
          value: (
            <>
              {count(m.tokens.in)} in · {count(m.tokens.out)} out{m.tokens.cached !== undefined ? ` · ${count(m.tokens.cached)} cached` : ""}
            </>
          ),
        }
      : null,
    m.costUsd !== undefined ? { label: "Cost", value: usd(m.costUsd, m.costEstimated) } : null,
    {
      label: "Criteria",
      value: (
        <EntryLink id="criteria" className="plain">
          {m.criteria.passed} of {m.criteria.total} passed
        </EntryLink>
      ),
    },
    { label: "Trigger", value: m.trigger },
    m.pushedBy ? { label: "Pushed by", value: m.pushedBy } : null,
    m.schemaVersion ? { label: "Trajectory", value: <span className="mono">{m.schemaVersion}</span> } : null,
  ];
}

function TrialBody({ s, job }: { s: TrialSession; job?: string }) {
  const h = s.header;
  const t = s.trial;
  const q = job ? `?job=${encodeURIComponent(job)}` : "";
  const tabs = [
    { to: "", label: "Log", count: s.log.entries.length, end: true },
    { to: "files", label: "Files", count: s.files.length, end: false },
    { to: "judge", label: "Judge", count: s.judge.length, end: false },
    { to: "network", label: "Network", count: s.network ? s.network.flows.length : undefined, end: false },
  ];
  const r = s.redaction;
  const redacted = r.exact + r.pattern + r.envDump + r.render;
  return (
    <Page
      title={t.id}
      docTitle={`${t.id} ${armLabel(t.arm)}`}
      sub={
        <>
          <Link to={runHref(s.run.name, s.run.currentJob ? undefined : s.run.job)}>
            {s.run.skill} {s.run.version}
          </Link>{" "}
          · <span className="mono">{s.run.entry}</span> · case <span className="mono">{t.case}</span>, attempt {t.attempt}, {armLabel(t.arm)}
        </>
      }
      aside={
        h.status === "scored" ? (
          <span className="score-head">
            <Mark ok={!!t.passed} okText="passed" badText="did not pass" />
            <span className="score-big num">{score(h.score)}</span>
            {h.armMean !== undefined ? <span className="muted small">arm mean {score(h.armMean)}</span> : null}
          </span>
        ) : (
          <Pill tone="bad" icon="✕">
            {h.status}
          </Pill>
        )
      }
    >
      {h.error ? (
        <Notice tone="bad" icon="✕" role="alert">
          <strong>{h.error.type}</strong>
          {h.error.occurredAt ? <span className="muted"> at {time(h.error.occurredAt)}</span> : null}
          <FoldText text={h.error.message} />
          {h.error.traceback ? (
            <details className="disclosure">
              <summary>Traceback</summary>
              <pre className="entry-text">{h.error.traceback}</pre>
            </details>
          ) : null}
        </Notice>
      ) : null}
      <PromptBox text={h.prompt} />
      <MetaStrip label="Trial metadata" items={metadataItems(s)} />
      {s.metadata.phases.length ? <PhaseBar phases={s.metadata.phases} /> : null}
      {h.dimensions.length ? (
        <dl className="dim-strip" aria-label="Scores of this trial">
          {h.dimensions.map((d) => (
            <div key={d.key}>
              <dt>{d.label}</dt>
              <dd>
                <span className="num">{score(d.score)}</span>
                <ScoreMeter value={d.score} mean={d.armMean} />
                {d.armMean !== undefined && d.armMean !== d.score ? <span className="muted small"> mean {score(d.armMean)}</span> : null}
              </dd>
            </div>
          ))}
        </dl>
      ) : null}
      <div className="tabs-bar">
        <nav aria-label="Trial views">
          <ul className="tabs">
            {tabs.map((tab) => (
              <li key={tab.label}>
                <NavLink to={`${tab.to}${q}`} end={tab.end}>
                  {tab.label}
                  {tab.count !== undefined ? (
                    <span className="tab-count">
                      <span className="sr-only">, </span>
                      {tab.count}
                    </span>
                  ) : null}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
        <div className="tabs-extra">
          {s.downloads.trajectory ? (
            // A server response (an attachment), not an app route: full page load.
            <a href={s.downloads.trajectory} download>
              trajectory.json
            </a>
          ) : null}
          <span className="muted small" title={`Redaction ${r.version}: ${r.exact} exact, ${r.pattern} pattern, ${r.envDump} environment dump, ${r.render} at render`}>
            {redacted === 0 ? "nothing redacted" : `${plural(redacted, "value")} redacted`}
          </span>
          {s.truncations.length ? <span className="muted small">{plural(s.truncations.length, "output")} shortened in the bundle</span> : null}
        </div>
      </div>
      <Outlet context={s} />
    </Page>
  );
}

function useSession(): TrialSession {
  return useOutletContext<TrialSession>();
}

/** TrialLogTab is the session log with category chips, eval criteria and findings. */
export function TrialLogTab() {
  const s = useSession();
  const empty =
    s.header.status === "scored"
      ? "This trial has no log entries."
      : "This trial has no trajectory: it stopped before the agent ran, so there is no session to show.";
  return (
    <>
      <nav className="skips" aria-label="Skip links">
        <a href="#session-log">Skip to the log</a>
        <a href="#criteria">Skip to the eval criteria</a>
      </nav>
      <div className="session">
        <section className="session-log" id="session-log" aria-label="Log" tabIndex={-1}>
          <LogView log={s.log} empty={empty} />
        </section>
        <aside className="session-aside" aria-label="Findings and criteria">
          <FindingsPanel findings={s.findings} />
          <CriteriaPanel criteria={s.criteria} />
        </aside>
      </div>
    </>
  );
}

const OP: Record<FileChange["op"], string> = { write: "written", edit: "edited", patch: "patched", command: "written by a command" };

/** FileView is one file change: full content, edit hunks, a patch, or a note that a command wrote it. */
export function FileView({ f, link = true }: { f: FileChange; link?: boolean }) {
  return (
    <article className="file">
      <header className="file-head">
        <span className="mono strong">{f.path}</span>
        <span className="tag">{OP[f.op]}</span>
        <span className="muted small">by {f.tool}</span>
        {link ? (
          <EntryLink id={f.entryId} className="step-chip" label={`Step ${f.step}`}>
            {f.step}
          </EntryLink>
        ) : (
          <span className="muted small">step {f.step}</span>
        )}
      </header>
      {!f.captured ? (
        <p className="muted">Written by a command at step {f.step}; the content was not captured.</p>
      ) : f.content !== undefined ? (
        <pre className="entry-text file-content">{f.content}</pre>
      ) : f.patch !== undefined ? (
        <pre className="entry-text file-content diff">
          {f.patch.split("\n").map((line, i) => (
            <span key={i} className={line.startsWith("+") ? "diff-add" : line.startsWith("-") ? "diff-del" : undefined}>
              {line}
              {"\n"}
            </span>
          ))}
        </pre>
      ) : (
        f.hunks.map((hk, i) => (
          <div key={i} className="hunk">
            <pre className="entry-text diff">
              {hk.old.split("\n").map((line, j) => (
                <span key={`o${j}`} className="diff-del">
                  <span aria-hidden="true">- </span>
                  <span className="sr-only">removed: </span>
                  {line}
                  {"\n"}
                </span>
              ))}
              {hk.new.split("\n").map((line, j) => (
                <span key={`n${j}`} className="diff-add">
                  <span aria-hidden="true">+ </span>
                  <span className="sr-only">added: </span>
                  {line}
                  {"\n"}
                </span>
              ))}
            </pre>
          </div>
        ))
      )}
      {f.truncated ? <p className="muted small">The content was cut for the page; the trajectory download has all of it.</p> : null}
    </article>
  );
}

/** TrialFilesTab lists the files the agent wrote or edited. */
export function TrialFilesTab() {
  const s = useSession();
  return (
    <Section title="Files">
      <p className="muted">
        Built from the agent&apos;s tool calls. The workspace itself is not collected, so this is what the calls wrote, in order.
      </p>
      {s.files.length === 0 ? <Empty>The agent wrote no files.</Empty> : s.files.map((f, i) => <FileView key={`${f.entryId}-${i}`} f={f} />)}
    </Section>
  );
}

function JudgeView({ j }: { j: JudgeMetric }) {
  return (
    <article className="judge" id={`judge-${j.key}`}>
      <header className="judge-head">
        <h3>{j.label}</h3>
        {j.score !== undefined ? (
          <>
            <span className="num strong">{score(j.score)}</span>
            <ScoreMeter value={j.score} />
          </>
        ) : null}
        {j.passed !== undefined ? <PassBadge passed={j.passed} /> : null}
        {j.model ? <span className="muted small mono">{j.model}</span> : null}
        <EntryLink id={`j-${j.key}`} className="small">
          in the log
        </EntryLink>
      </header>
      {j.reason ? <FoldText text={j.reason} /> : null}
      <StepChips steps={j.steps} label={`Steps ${j.label} cites`} />
      {j.details !== undefined ? (
        <details className="disclosure">
          <summary>Details as stored in reward.json</summary>
          <pre className="entry-text">{JSON.stringify(j.details, null, 2)}</pre>
        </details>
      ) : null}
    </article>
  );
}

/** TrialJudgeTab shows every judge metric with its details. */
export function TrialJudgeTab() {
  const s = useSession();
  return (
    <Section title="Judge">
      {s.judge.length === 0 ? <Empty>No judge results: the trial was not scored.</Empty> : s.judge.map((j) => <JudgeView key={j.key} j={j} />)}
    </Section>
  );
}

const NET_STATE: Record<TrialNetwork["state"], string> = {
  collected: "Collected",
  partial: "Partly collected",
  unavailable: "Not available",
  off: "Off",
};

/** TrialNetworkTab shows DNS names and flows. */
export function TrialNetworkTab() {
  const s = useSession();
  const n = s.network;
  if (!n)
    return (
      <Section title="Network">
        <Empty>No network data in this trial bundle (flows were not collected for this run).</Empty>
      </Section>
    );
  const ok = n.state === "collected";
  return (
    <Section title="Network">
      <Notice tone={ok ? "ok" : n.state === "partial" ? "warn" : "neutral"} icon={ok ? "✓" : n.state === "partial" ? "!" : "○"}>
        <strong>{NET_STATE[n.state]}</strong>
        {n.reason ? `: ${n.reason}` : null}
        {n.from && n.to ? (
          <span className="muted">
            {" "}
            · {clock(n.from)} to {clock(n.to)} UTC
          </span>
        ) : null}
        {n.lost > 0 ? <span> · {plural(n.lost, "event")} lost</span> : null}
        {n.truncated ? <span> · the list was capped</span> : null}
      </Notice>
      <h3 className="subhead">Flows</h3>
      {n.flows.length === 0 ? (
        <Empty>No flows.</Empty>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <caption className="sr-only">Connections of this trial</caption>
            <thead>
              <tr>
                <th scope="col">Verdict</th>
                <th scope="col">Destination</th>
                <th scope="col">IP</th>
                <th scope="col">Port</th>
                <th scope="col" className="num">
                  Count
                </th>
                <th scope="col">First</th>
                <th scope="col">Last</th>
                <th scope="col">Reason</th>
                <th scope="col">Step</th>
              </tr>
            </thead>
            <tbody>
              {n.flows.map((f, i) => (
                <tr key={`${f.destination}-${f.port}-${f.verdict}-${i}`} className={f.verdict === "DROPPED" ? "row-bad" : undefined}>
                  <td>
                    {f.verdict === "FORWARDED" ? (
                      <Pill tone="ok" icon="✓">
                        FORWARDED
                      </Pill>
                    ) : f.verdict === "DROPPED" ? (
                      <Pill tone="bad" icon="✕">
                        DROPPED
                      </Pill>
                    ) : (
                      <Pill tone="warn" icon="!">
                        {f.verdict}
                      </Pill>
                    )}
                  </td>
                  <td className="mono">{f.destination}</td>
                  <td className="mono">{f.ip}</td>
                  <td className="mono">
                    {f.protocol}/{f.port}
                  </td>
                  <td className="num">{f.count}</td>
                  <td className="nowrap">{clock(f.firstAt)}</td>
                  <td className="nowrap">{clock(f.lastAt)}</td>
                  <td>{f.reason ?? <span className="muted">-</span>}</td>
                  <td>
                    {f.step > 0 ? (
                      <EntryLink id={`s-${f.step}`} className="step-chip" label={`Step ${f.step}`}>
                        {f.step}
                      </EntryLink>
                    ) : (
                      <span className="muted">-</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <h3 className="subhead">DNS names</h3>
      {n.dns.length === 0 ? (
        <Empty>No DNS queries.</Empty>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <caption className="sr-only">DNS names the trial asked for</caption>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col" className="num">
                  Count
                </th>
                <th scope="col">Answer</th>
                <th scope="col">First</th>
                <th scope="col">Last</th>
              </tr>
            </thead>
            <tbody>
              {n.dns.map((d) => (
                <tr key={d.name}>
                  <td className="mono">{d.name}</td>
                  <td className="num">{d.count}</td>
                  <td>{d.rcode ?? <span className="muted">-</span>}</td>
                  <td className="nowrap">{clock(d.firstAt)}</td>
                  <td className="nowrap">{clock(d.lastAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  );
}
