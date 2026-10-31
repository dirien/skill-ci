import { Link } from "react-router";

import { api } from "../api/client.ts";
import type { AttentionItem, ChannelRow, NetworkPolicy, Overview } from "../api/types.ts";
import { useApi } from "../api/useApi.ts";
import { Empty, Loaded, MetaStrip, Notice, Page, Section } from "../components/Page.tsx";
import { RunsTable, runHref } from "../components/RunsTable.tsx";
import { Pill, type Tone } from "../components/Status.tsx";
import { time } from "../format.ts";

/** OverviewPage is "/": counts, needs attention, recent runs, release channels, network policy. */
export function OverviewPage() {
  const state = useApi((signal) => api.overview(signal), []);
  return (
    <Page title="Overview">
      <Loaded state={state}>{(o) => <OverviewBody o={o} />}</Loaded>
    </Page>
  );
}

function OverviewBody({ o }: { o: Overview }) {
  const c = o.counts;
  const tiles: [string, number, string?][] = [
    ["Runs", c.runs],
    ["Running", c.running],
    ["Succeeded", c.succeeded],
    ["Failed", c.failed],
    ["Skills", c.skills],
    ["On stable", c.onStable],
    ["Held back", c.heldBack],
    ["Rejected", c.rejected],
  ];
  return (
    <>
      <dl className="counts">
        {tiles.map(([label, value]) => (
          <div key={label} className={value > 0 && (label === "Failed" || label === "Rejected") ? "count count-bad" : "count"}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
      <Section title="Needs attention" extra={<span className="muted">{o.attention.length} items</span>}>
        {o.attention.length === 0 ? <Empty>Nothing needs attention.</Empty> : <AttentionList items={o.attention} />}
      </Section>
      <Section title="Recent runs" extra={<Link to="/runs">All runs</Link>}>
        {o.recent.length === 0 ? <Empty>No runs yet.</Empty> : <RunsTable runs={o.recent} caption="The 20 newest runs" />}
      </Section>
      <Section title="Release channels">
        {o.channelsError ? (
          <Notice tone="warn" icon="!">
            Harbor tags could not be read: {o.channelsError}
          </Notice>
        ) : null}
        <ChannelTable rows={o.channels} />
      </Section>
      <NetworkPolicyView n={o.network} />
    </>
  );
}

const ATTENTION: Record<AttentionItem["kind"], [Tone, string, string]> = {
  failed: ["bad", "✕", "Failed"],
  "held-back": ["warn", "!", "Held back"],
  rejected: ["bad", "✕", "Rejected"],
  running: ["info", "◐", "Running"],
};

function AttentionList({ items }: { items: AttentionItem[] }) {
  return (
    <ul className="attention">
      {items.map((a) => {
        const [tone, icon, word] = ATTENTION[a.kind];
        return (
          <li key={`${a.kind}-${a.skill}-${a.version}-${a.run ?? ""}`}>
            <Pill tone={tone} icon={icon}>
              {word}
            </Pill>
            <div className="attention-body">
              <div>
                {a.run ? (
                  <Link to={runHref(a.run)} title={a.run}>
                    <span className="strong">{a.skill}</span> {a.version}
                  </Link>
                ) : (
                  <span>
                    <span className="strong">{a.skill}</span> {a.version}
                  </span>
                )}
                {a.entry ? <span className="mono muted"> {a.entry}</span> : null}
                <span className="muted"> · since {time(a.since)}</span>
              </div>
              <div className="clamp-2" title={a.message}>
                {a.message}
              </div>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

function versions(list: string[]) {
  return list.length === 0 ? <span className="muted">-</span> : list.join(", ");
}

function ChannelTable({ rows }: { rows: ChannelRow[] }) {
  return (
    <div className="table-wrap">
      <table className="table">
        <caption className="sr-only">Release channels per skill</caption>
        <thead>
          <tr>
            <th scope="col">Skill</th>
            <th scope="col">Stable</th>
            <th scope="col">Beta</th>
            <th scope="col">Held back</th>
            <th scope="col">Rejected</th>
            <th scope="col" className="num">
              Versions
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.skill}>
              <th scope="row">
                <Link to={`/skills/${encodeURIComponent(r.skill)}`}>{r.skill}</Link>
              </th>
              <td className="mono">{r.stable ?? <span className="muted">-</span>}</td>
              <td className="mono">{r.beta ?? <span className="muted">-</span>}</td>
              <td className="mono">{versions(r.heldBack)}</td>
              <td className="mono">{versions(r.rejected)}</td>
              <td className="num">{r.versions}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function NetworkPolicyView({ n }: { n: NetworkPolicy }) {
  return (
    <Section title="Network policy">
      {n.problem ? (
        <Notice tone="warn" icon="!">
          The SkillCIConfig is invalid: {n.problem}
        </Notice>
      ) : null}
      <details className="disclosure">
        <summary>
          Default <code>{n.default}</code>, enforced by {n.enforcedBy ?? n.enforcement}, from {n.sourceText}
        </summary>
        <MetaStrip
          label="Network policy"
          items={[
            { label: "Default mode", value: <code>{n.default}</code> },
            { label: "Allowed modes", value: n.modes.join(", ") },
            { label: "Bundles", value: n.bundles.length ? n.bundles.join(", ") : "none" },
            { label: "Enforcement", value: n.enforcedBy ? `${n.enforcement} (${n.enforcedBy})` : n.enforcement },
            { label: "Hubble Relay", value: n.hubbleRelay ? <code>{n.hubbleRelay}</code> : "not configured, no flows" },
            { label: "Source", value: n.sourceText },
          ]}
        />
      </details>
    </Section>
  );
}
