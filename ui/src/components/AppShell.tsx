// The app shell: skip link, top bar with navigation and the theme toggle,
// breadcrumbs from the matched routes, and the page.
import { useState } from "react";
import { NavLink, Outlet, useMatches } from "react-router";

import { api } from "../api/client.ts";
import { useApi } from "../api/useApi.ts";
import type { Crumb } from "../routes.tsx";
import { nextTheme, readTheme, saveTheme, type ThemeChoice } from "../theme.ts";

const NAV = [
  { to: "/", label: "Overview", end: true },
  { to: "/runs", label: "Runs", end: false },
  { to: "/skills", label: "Skills", end: false },
  { to: "/models", label: "Models", end: false },
];

interface CrumbHandle {
  crumb?: (params: Record<string, string | undefined>) => Crumb;
}

export function Breadcrumbs() {
  const crumbs = useMatches()
    .map((m) => {
      const handle = m.handle as CrumbHandle | undefined;
      return handle?.crumb ? { ...handle.crumb(m.params), id: m.id } : null;
    })
    .filter((c): c is Crumb & { id: string } => c !== null);
  if (crumbs.length === 0) return null;
  return (
    <nav className="crumbs" aria-label="Breadcrumb">
      <ol>
        {crumbs.map((c, i) =>
          i === crumbs.length - 1 ? (
            <li key={c.id} aria-current="page">
              {c.label}
            </li>
          ) : (
            <li key={c.id}>
              <NavLink to={c.to} end>
                {c.label}
              </NavLink>
            </li>
          ),
        )}
      </ol>
    </nav>
  );
}

export function ThemeToggle() {
  const [choice, setChoice] = useState<ThemeChoice>(readTheme);
  const next = nextTheme(choice);
  return (
    <button
      type="button"
      className="theme-toggle"
      aria-label={`Theme: ${choice}. Switch to ${next}.`}
      onClick={() => {
        saveTheme(next);
        setChoice(next);
      }}
    >
      <span aria-hidden="true">{choice === "dark" ? "◐" : choice === "light" ? "○" : "◑"}</span> Theme: {choice}
    </button>
  );
}

export function AppShell() {
  const site = useApi((signal) => api.site(signal), []);
  return (
    <>
      <a className="skip" href="#main">
        Skip to content
      </a>
      <header className="topbar">
        <NavLink className="brand" to="/" end aria-label="skill-ci overview">
          <span className="brand-mark" aria-hidden="true">
            ▸
          </span>{" "}
          skill-ci
        </NavLink>
        <nav aria-label="Main">
          <ul className="nav">
            {NAV.map((n) => (
              <li key={n.to}>
                <NavLink to={n.to} end={n.end}>
                  {n.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
        <span className="meta">
          {site.status === "ok" ? (
            <>
              namespace <code>{site.data.namespace}</code> · Harbor project <code>{site.data.project}</code> · controller{" "}
              <code>{site.data.version}</code>
            </>
          ) : null}
        </span>
        <ThemeToggle />
      </header>
      <Breadcrumbs />
      <main id="main" tabIndex={-1}>
        <Outlet />
      </main>
      <footer className="foot">Read-only view. SkillEvals from the cluster, tags from Harbor, reports and trials from OCI referrers.</footer>
    </>
  );
}
