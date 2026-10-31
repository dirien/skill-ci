import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { RunList } from "../api/types.ts";
import { renderAt, stubApi } from "../test/render.tsx";
import { readQuery } from "./RunsPage.tsx";

describe("runs page", () => {
  it("lists every run with its columns", async () => {
    renderAt("/runs");
    await screen.findByText("7 runs");
    const table = screen.getByRole("table", { name: "Runs" });
    const head = within(table).getAllByRole("columnheader").map((h) => h.textContent);
    expect(head).toEqual(["Run", "Agent", "Model", "Status", "Verdict", "Lift", "Tiers", "Publication", "Trials", "Created"]);
    expect(within(table).getAllByRole("row")).toHaveLength(8);
    expect(within(table).getByText("codex@0.161.0")).toBeTruthy();
    expect(within(table).getAllByText("sessions").length).toBe(4);
  });

  it("keeps filters in the URL and asks the API with them", async () => {
    const { router } = renderAt("/runs?skill=acme-release-notes");
    await screen.findByText(/filtered by skill/);
    const skill = screen.getByRole<HTMLSelectElement>("combobox", { name: "Skill" });
    expect(skill.value).toBe("acme-release-notes");

    fireEvent.change(screen.getByRole("combobox", { name: "Verdict" }), { target: { value: "Helps" } });
    await waitFor(() => expect(router.state.location.search).toBe("?skill=acme-release-notes&verdict=Helps"));

    fireEvent.change(screen.getByRole("combobox", { name: "Sort" }), { target: { value: "lift" } });
    await waitFor(() => expect(router.state.location.search).toContain("sort=lift"));

    const q = screen.getByRole("searchbox", { name: /Name contains/ });
    fireEvent.change(q, { target: { value: "codex" } });
    fireEvent.submit(q.closest("form") as HTMLFormElement);
    await waitFor(() => expect(router.state.location.search).toContain("q=codex"));
  });

  it("builds the API query from the URL in a fixed order", () => {
    expect(readQuery(new URLSearchParams("page=2&verdict=Helps&skill=x&sort=lift&junk=1"))).toEqual({
      skill: "x",
      verdict: "Helps",
      sort: "lift",
      page: 2,
    });
    expect(readQuery(new URLSearchParams("sort=bogus&page=0"))).toEqual({});
  });

  it("pages through the runs with links that keep the filters", async () => {
    stubApi("/runs", (body) => {
      const list = body as RunList;
      return { ...list, pagination: { page: 2, pageSize: 2, total: 7, pages: 4 } };
    });
    const { router } = renderAt("/runs?skill=acme-release-notes&page=2");
    const pager = await screen.findByRole("navigation", { name: "Pages" });
    expect(within(pager).getByText("2").getAttribute("aria-current")).toBe("page");
    fireEvent.click(within(pager).getByRole("link", { name: "Next" }));
    await waitFor(() => expect(router.state.location.search).toBe("?skill=acme-release-notes&page=3"));
  });

  it("focuses the name filter on /", async () => {
    renderAt("/runs");
    await screen.findByText("7 runs");
    fireEvent.keyDown(document, { key: "/" });
    expect(document.activeElement).toBe(screen.getByRole("searchbox", { name: /Name contains/ }));
  });
});
