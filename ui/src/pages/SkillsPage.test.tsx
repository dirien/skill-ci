import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { RUN } from "../api/fixtures/index.ts";
import { renderAt } from "../test/render.tsx";

describe("skills page", () => {
  it("lists the catalog", async () => {
    renderAt("/skills");
    const table = await screen.findByRole("table", { name: "Skills" });
    const row = within(table).getByRole("rowheader", { name: "python-docstrings" }).closest("tr") as HTMLElement;
    expect(within(row).getByText("v1.1.0", { selector: ".pill" })).toBeTruthy();
    expect(within(row).getByText("0 of 1")).toBeTruthy();
    const k8s = within(table).getByRole("rowheader", { name: "k8s-resource-naming" }).closest("tr") as HTMLElement;
    expect(within(k8s).getByText("1 running")).toBeTruthy();
  });
});

describe("skill page", () => {
  it("shows the versions by entries matrix and the policy decisions", async () => {
    renderAt("/skills/acme-release-notes");
    const matrix = await screen.findByRole("table", { name: /Versions of acme-release-notes/ });
    const head = within(matrix).getAllByRole("columnheader");
    expect(head).toHaveLength(5);
    expect(head[1]?.textContent).toContain("claude-code@2.1.292");

    const v110 = within(matrix).getByRole("rowheader", { name: /v1\.1\.0/ }).closest("tr") as HTMLElement;
    expect(within(v110).getByText("beta")).toBeTruthy();
    expect(within(v110).getByRole("link", { name: `Run ${RUN.claudeCode}` }).getAttribute("href")).toBe(`/runs/${RUN.claudeCode}`);
    expect(within(v110).getByText("Meets threshold")).toBeTruthy();
    expect(within(v110).getAllByText("no interval").length).toBe(3);

    const v100 = within(matrix).getByRole("rowheader", { name: /v1\.0\.0/ }).closest("tr") as HTMLElement;
    expect(within(v100).getByText("95% CI +0.51 to +0.65, n=3/3")).toBeTruthy();
    expect(within(v100).getAllByText("not run")).toHaveLength(2);
    expect(screen.getByText("How to read this")).toBeTruthy();
  });

  it("answers an unknown skill with the API's 404", async () => {
    renderAt("/skills/nope");
    expect((await screen.findByRole("alert")).textContent).toContain("Not found");
  });
});

describe("models page", () => {
  it("lists the model list entries with their run statistics", async () => {
    renderAt("/models");
    const table = await screen.findByRole("table", { name: "Model list entries" });
    const codex = within(table).getByRole("rowheader", { name: "codex@0.161.0" }).closest("tr") as HTMLElement;
    expect(within(codex).getByText("+0.04")).toBeTruthy();
    expect(within(codex).getByText("Neutral")).toBeTruthy();
    const claude = within(table).getByRole("rowheader", { name: "claude-code@2.1.292" }).closest("tr") as HTMLElement;
    expect(within(claude).getByText("required")).toBeTruthy();
    expect(within(claude).getByRole("link", { name: "5" }).getAttribute("href")).toBe(
      "/runs?agent=claude-code%402.1.292&model=claude-haiku-5-5",
    );
  });
});
