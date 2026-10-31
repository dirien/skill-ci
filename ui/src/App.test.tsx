import { fireEvent, render, screen, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { describe, expect, it } from "vitest";

import { ApiError, api } from "./api/client.ts";
import { RUN } from "./api/fixtures/index.ts";
import { routes } from "./routes.tsx";
import { nextTheme, readTheme, saveTheme } from "./theme.ts";

function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  render(<RouterProvider router={router} />);
  return router;
}

describe("app shell", () => {
  it("renders the overview from the fixtures", async () => {
    renderAt("/");
    expect(await screen.findByRole("heading", { level: 1, name: "Overview" })).toBeTruthy();
    const nav = screen.getByRole("navigation", { name: "Main" });
    for (const name of ["Overview", "Runs", "Skills", "Models"]) {
      expect(within(nav).getByRole("link", { name })).toBeTruthy();
    }
    expect(await screen.findByText("skill-ci", { selector: "code" })).toBeTruthy();
    const runs = await screen.findByText("Runs", { selector: "dt" });
    expect(runs.nextElementSibling?.textContent).toBe("8");
  });

  it("builds breadcrumbs from the routes", async () => {
    renderAt(`/runs/${RUN.claudeCode}/trials/rn-basic__sryqofs/files`);
    const crumbs = await screen.findByRole("navigation", { name: "Breadcrumb" });
    expect(within(crumbs).getAllByRole("link").map((a) => a.textContent)).toEqual(["Overview", "Runs", RUN.claudeCode]);
    expect(within(crumbs).getByRole("link", { name: RUN.claudeCode }).getAttribute("href")).toBe(`/runs/${RUN.claudeCode}`);
    expect(within(crumbs).getByText("rn-basic__sryqofs").getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("link", { name: /^Files/ }).getAttribute("aria-current")).toBe("page");
  });

  it("answers unknown paths with the not found page", async () => {
    renderAt("/no/such/page");
    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeTruthy();
  });
});

describe("api client", () => {
  it("returns typed fixtures and raises the API's errors", async () => {
    const trial = await api.trial(RUN.claudeCode, "rn-basic__sryqofs");
    expect(trial.trial.arm).toBe("with-skill");
    expect(trial.log.counts.map((c) => c.category)).toContain("judge");
    const err = await api.run("nope").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("not_found");
  });
});

describe("theme", () => {
  it("toggles from the top bar: system, light, dark, and back", async () => {
    renderAt("/");
    const button = await screen.findByRole("button", { name: "Theme: system. Switch to light." });
    fireEvent.click(button);
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    fireEvent.click(screen.getByRole("button", { name: "Theme: light. Switch to dark." }));
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(window.localStorage.getItem("skillci.theme")).toBe("dark");
    fireEvent.click(screen.getByRole("button", { name: "Theme: dark. Switch to system." }));
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(window.localStorage.getItem("skillci.theme")).toBeNull();
  });

  it("cycles and survives storage that throws", () => {
    expect(nextTheme("system")).toBe("light");
    saveTheme("dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(readTheme()).toBe("dark");
    saveTheme("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    const original = Object.getOwnPropertyDescriptor(window, "localStorage");
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      get() {
        throw new Error("blocked");
      },
    });
    try {
      expect(readTheme()).toBe("system");
      expect(() => saveTheme("light")).not.toThrow();
    } finally {
      if (original) Object.defineProperty(window, "localStorage", original);
    }
  });
});
