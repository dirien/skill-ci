import { NavLink, Outlet, useParams } from "react-router";

import { Page, Todo } from "../components/Page.tsx";

const TABS = [
  { to: "", label: "Log", end: true },
  { to: "files", label: "Files", end: false },
  { to: "judge", label: "Judge", end: false },
  { to: "network", label: "Network", end: false },
];

/** TrialLayout is /runs/{name}/trials/{trial}[?job=]: header, metadata and the tabs. */
export function TrialLayout() {
  const { trial = "" } = useParams();
  return (
    <Page title={trial}>
      <Todo what="the session header and metadata strip" />
      <nav aria-label="Trial views">
        <ul className="tabs">
          {TABS.map((t) => (
            <li key={t.label}>
              <NavLink to={t.to} end={t.end}>
                {t.label}
              </NavLink>
            </li>
          ))}
        </ul>
      </nav>
      <Outlet />
    </Page>
  );
}

/** TrialLogTab is the session log with category chips, eval criteria and findings. */
export function TrialLogTab() {
  return <Todo what="the session log, eval criteria and session findings" />;
}

/** TrialFilesTab lists the files the agent wrote or edited. */
export function TrialFilesTab() {
  return <Todo what="the files view" />;
}

/** TrialJudgeTab shows every judge metric with its details. */
export function TrialJudgeTab() {
  return <Todo what="the judge details" />;
}

/** TrialNetworkTab shows DNS names and flows. */
export function TrialNetworkTab() {
  return <Todo what="the network flows" />;
}
