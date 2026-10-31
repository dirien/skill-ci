// The routes of the app (CONTRACT.md, "Reporting site"). Each route's handle
// gives its breadcrumb. /runs/{name}/report and /runs/{name}/result.json are
// server pages, not routes: link to them with reloadDocument.
import type { RouteObject } from "react-router";

import { AppShell } from "./components/AppShell.tsx";
import { ComparePage } from "./pages/ComparePage.tsx";
import { ModelsPage } from "./pages/ModelsPage.tsx";
import { NotFoundPage } from "./pages/NotFoundPage.tsx";
import { OverviewPage } from "./pages/OverviewPage.tsx";
import { RunPage } from "./pages/RunPage.tsx";
import { RunsPage } from "./pages/RunsPage.tsx";
import { SkillPage } from "./pages/SkillPage.tsx";
import { SkillsPage } from "./pages/SkillsPage.tsx";
import { TrialFilesTab, TrialJudgeTab, TrialLayout, TrialLogTab, TrialNetworkTab } from "./pages/TrialPage.tsx";

/** Crumb is one breadcrumb step. */
export interface Crumb {
  label: string;
  to: string;
}

type Params = Record<string, string | undefined>;
// A route may add more than one step: a run page sits under Runs although its
// route is not a child of /runs.
const crumb = (f: (p: Params) => Crumb | Crumb[]) => ({ crumb: f });
const RUNS: Crumb = { label: "Runs", to: "/runs" };
const SKILLS: Crumb = { label: "Skills", to: "/skills" };
const enc = (s: string | undefined) => encodeURIComponent(s ?? "");

export const routes: RouteObject[] = [
  {
    path: "/",
    element: <AppShell />,
    handle: crumb(() => ({ label: "Overview", to: "/" })),
    children: [
      { index: true, element: <OverviewPage /> },
      { path: "runs", element: <RunsPage />, handle: crumb(() => RUNS) },
      {
        path: "runs/:name",
        handle: crumb((p) => [RUNS, { label: p.name ?? "", to: `/runs/${enc(p.name)}` }]),
        children: [
          { index: true, element: <RunPage /> },
          {
            path: "trials/:trial",
            element: <TrialLayout />,
            handle: crumb((p) => ({ label: p.trial ?? "", to: `/runs/${enc(p.name)}/trials/${enc(p.trial)}` })),
            children: [
              { index: true, element: <TrialLogTab /> },
              { path: "files", element: <TrialFilesTab /> },
              { path: "judge", element: <TrialJudgeTab /> },
              { path: "network", element: <TrialNetworkTab /> },
            ],
          },
          {
            path: "cases/:case/compare",
            element: <ComparePage />,
            handle: crumb((p) => ({ label: `Compare ${p.case ?? ""}`, to: `/runs/${enc(p.name)}/cases/${enc(p.case)}/compare` })),
          },
        ],
      },
      { path: "skills", element: <SkillsPage />, handle: crumb(() => SKILLS) },
      { path: "skills/:skill", element: <SkillPage />, handle: crumb((p) => [SKILLS, { label: p.skill ?? "", to: `/skills/${enc(p.skill)}` }]) },
      { path: "models", element: <ModelsPage />, handle: crumb(() => ({ label: "Models", to: "/models" })) },
      { path: "*", element: <NotFoundPage />, handle: crumb(() => ({ label: "Not found", to: "/" })) },
    ],
  },
];
