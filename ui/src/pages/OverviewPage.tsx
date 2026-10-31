import { api } from "../api/client.ts";
import { useApi } from "../api/useApi.ts";
import { Loaded, Page, Todo } from "../components/Page.tsx";

/** OverviewPage is "/": counts, needs attention, recent runs, network policy. */
export function OverviewPage() {
  const state = useApi((signal) => api.overview(signal), []);
  return (
    <Page title="Overview">
      <Loaded state={state}>
        {(o) => (
          <dl className="counts">
            <div>
              <dt>Runs</dt>
              <dd>{o.counts.runs}</dd>
            </div>
            <div>
              <dt>Skills</dt>
              <dd>{o.counts.skills}</dd>
            </div>
            <div>
              <dt>Failed</dt>
              <dd>{o.counts.failed}</dd>
            </div>
            <div>
              <dt>Rejected</dt>
              <dd>{o.counts.rejected}</dd>
            </div>
          </dl>
        )}
      </Loaded>
      <Todo what="needs attention, recent runs, release channels, network policy" />
    </Page>
  );
}
