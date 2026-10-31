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
const crumb = (f: (p: Params) => Crumb) => ({ crumb: f });
const enc = (s: string | undefined) => encodeURIComponent(s ?? "");

export const routes: RouteObject[] = [
  {
    path: "/",
    element: <AppShell />,
    handle: crumb(() => ({ label: "Overview", to: "/" })),
    children: [
      { index: true, element: <OverviewPage /> },
      { path: "runs", element: <RunsPage />, handle: crumb(() => ({ label: "Runs", to: "/runs" })) },
      {
        path: "runs/:name",
        handle: crumb((p) => ({ label: p.name ?? "", to: `/runs/${enc(p.name)}` })),
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
      { path: "skills", element: <SkillsPage />, handle: crumb(() => ({ label: "Skills", to: "/skills" })) },
      { path: "skills/:skill", element: <SkillPage />, handle: crumb((p) => ({ label: p.skill ?? "", to: `/skills/${enc(p.skill)}` })) },
      { path: "models", element: <ModelsPage />, handle: crumb(() => ({ label: "Models", to: "/models" })) },
      { path: "*", element: <NotFoundPage />, handle: crumb(() => ({ label: "Not found", to: "/" })) },
    ],
  },
];
