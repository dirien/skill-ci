import { Link } from "react-router";

import { api } from "../api/client.ts";
import { useApi } from "../api/useApi.ts";
import { Empty, Loaded, Notice, Page } from "../components/Page.tsx";
import { phaseLook, Pill } from "../components/Status.tsx";
import { time } from "../format.ts";

/** SkillsPage is /skills: the skill catalog. */
export function SkillsPage() {
  const state = useApi((signal) => api.skills(signal), []);
  return (
    <Page title="Skills">
      <Loaded state={state}>
        {(list) => (
          <>
            {list.channelsError ? (
              <Notice tone="warn" icon="!">
                Harbor tags could not be read, so channels may be missing: {list.channelsError}
              </Notice>
            ) : null}
            {list.items.length === 0 ? (
              <Empty>No skills have been evaluated yet.</Empty>
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <caption className="sr-only">Skills</caption>
                  <thead>
                    <tr>
                      <th scope="col">Skill</th>
                      <th scope="col">Stable</th>
                      <th scope="col">Beta</th>
                      <th scope="col">Newest</th>
                      <th scope="col" className="num">
                        Versions
                      </th>
                      <th scope="col" className="num">
                        Runs
                      </th>
                      <th scope="col">Last run</th>
                      <th scope="col">Held back</th>
                      <th scope="col">Rejected</th>
                      <th scope="col">Required entries passing</th>
                    </tr>
                  </thead>
                  <tbody>
                    {list.items.map((s) => {
                      const allPass = s.required.total > 0 && s.required.passed === s.required.total;
                      return (
                        <tr key={s.skill}>
                          <th scope="row">
                            <Link to={`/skills/${encodeURIComponent(s.skill)}`}>{s.skill}</Link>
                          </th>
                          <td className="mono">{s.stable ?? <span className="muted">-</span>}</td>
                          <td className="mono">{s.beta ?? <span className="muted">-</span>}</td>
                          <td className="mono">{s.newest ?? <span className="muted">-</span>}</td>
                          <td className="num">{s.versions}</td>
                          <td className="num">
                            {s.runs}
                            {s.running > 0 ? (
                              <>
                                {" "}
                                <Pill tone={phaseLook("Running")[0]} icon={phaseLook("Running")[1]}>
                                  {s.running} running
                                </Pill>
                              </>
                            ) : null}
                            {s.pending > 0 ? (
                              <>
                                {" "}
                                <Pill tone={phaseLook("Pending")[0]} icon={phaseLook("Pending")[1]}>
                                  {s.pending} pending
                                </Pill>
                              </>
                            ) : null}
                          </td>
                          <td className="nowrap">{time(s.lastRunAt)}</td>
                          <td className="mono">{s.heldBack.length ? s.heldBack.join(", ") : <span className="muted">-</span>}</td>
                          <td className="mono">
                            {s.rejected.length ? (
                              <Pill tone="bad" icon="✕">
                                {s.rejected.join(", ")}
                              </Pill>
                            ) : (
                              <span className="muted">-</span>
                            )}
                          </td>
                          <td>
                            <Pill tone={allPass ? "ok" : s.required.total === 0 ? "neutral" : "warn"} icon={allPass ? "✓" : s.required.total === 0 ? "-" : "!"}>
                              {s.required.passed} of {s.required.total}
                            </Pill>
                          </td>
                        </tr>
                      );
                    })}
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
