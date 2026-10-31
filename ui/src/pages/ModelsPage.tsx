import { Link } from "react-router";

import { api } from "../api/client.ts";
import { useApi } from "../api/useApi.ts";
import { Empty, Loaded, Notice, Page } from "../components/Page.tsx";
import { Lift } from "../components/Lift.tsx";
import { Pill } from "../components/Status.tsx";
import { agentRef, time } from "../format.ts";

/** ModelsPage is /models: the model list entries with their run statistics. */
export function ModelsPage() {
  const state = useApi((signal) => api.models(signal), []);
  return (
    <Page title="Models">
      <Loaded state={state}>
        {(list) => (
          <>
            <p className="muted">
              From the ConfigMap <code>{list.source}</code>. Retired entries that still have runs are listed and marked.
            </p>
            {list.error ? (
              <Notice tone="warn" icon="!">
                The model list could not be read: {list.error}
              </Notice>
            ) : null}
            {list.items.length === 0 ? (
              <Empty>The model list is empty.</Empty>
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <caption className="sr-only">Model list entries</caption>
                  <thead>
                    <tr>
                      <th scope="col">Agent</th>
                      <th scope="col">Model</th>
                      <th scope="col">Required</th>
                      <th scope="col">Sandbox profile</th>
                      <th scope="col" className="num">
                        Runs
                      </th>
                      <th scope="col">Verdicts</th>
                      <th scope="col">Mean lift</th>
                      <th scope="col" className="num">
                        Failed
                      </th>
                      <th scope="col" className="num">
                        Retries
                      </th>
                      <th scope="col">Last run</th>
                    </tr>
                  </thead>
                  <tbody>
                    {list.items.map((m) => (
                      <tr key={m.entry} className={m.listed ? undefined : "row-retired"}>
                        <th scope="row" className="mono">
                          {agentRef(m.agent, m.agentVersion)}
                          {m.listed ? null : (
                            <>
                              {" "}
                              <span className="tag">retired</span>
                            </>
                          )}
                        </th>
                        <td className="mono">{m.model}</td>
                        <td>{m.required ? <Pill tone="info" icon="●">required</Pill> : <span className="muted">optional</span>}</td>
                        <td className="mono">{m.sandboxProfile ?? <span className="muted">default</span>}</td>
                        <td className="num">
                          {m.runs > 0 ? <Link to={`/runs?agent=${encodeURIComponent(agentRef(m.agent, m.agentVersion))}&model=${encodeURIComponent(m.model)}`}>{m.runs}</Link> : 0}
                        </td>
                        <td>
                          {Object.keys(m.verdicts).length === 0 ? (
                            <span className="muted">-</span>
                          ) : (
                            <ul className="inline-list">
                              {Object.entries(m.verdicts)
                                .sort(([a], [b]) => a.localeCompare(b))
                                .map(([v, n]) => (
                                  <li key={v}>
                                    {v} <span className="num">{n}</span>
                                  </li>
                                ))}
                            </ul>
                          )}
                        </td>
                        <td>
                          <Lift lift={m.meanLift} compact evidence={false} />
                        </td>
                        <td className="num">{m.failed > 0 ? <Pill tone="bad" icon="✕">{m.failed}</Pill> : 0}</td>
                        <td className="num">{m.retries}</td>
                        <td className="nowrap">{time(m.lastRunAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </Loaded>
    </Page>
  );
}
