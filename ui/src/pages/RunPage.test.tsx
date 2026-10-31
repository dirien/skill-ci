import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { RUN } from "../api/fixtures/index.ts";
import { renderAt } from "../test/render.tsx";

describe("run page", () => {
  it("shows scores, dimensions, report links and the trial grid", async () => {
    renderAt(`/runs/${RUN.claudeCode}`);
    expect(await screen.findByRole("heading", { level: 1, name: "acme-release-notes v1.1.0" })).toBeTruthy();
    const scores = screen.getByRole("region", { name: "Scores" });
    expect(within(scores).getByText("+0.59")).toBeTruthy();
    expect(within(scores).getByText("no interval")).toBeTruthy();

    const report = screen.getByRole("region", { name: "Report" });
    // Server pages, not routes: plain links (a full page load).
    expect(within(report).getByRole("link", { name: "report.html" }).getAttribute("href")).toBe(`/runs/${RUN.claudeCode}/report`);
    const dims = within(report).getByRole("table");
    expect(within(dims).getAllByRole("row")).toHaveLength(8);
    expect(within(report).getByRole("img", { name: "Goal accuracy: with skill 1.00, without skill 0.05" })).toBeTruthy();
    expect(within(report).getByRole("list", { name: "Legend" }).textContent).toBe("With skillWithout skill");

    const trials = screen.getByRole("region", { name: "Trials" });
    const grid = within(trials).getByRole("table", { name: /Trials of case rn-basic/ });
    const session = within(grid).getByRole("link", { name: "Session rn-basic__sryqofs, with-skill, attempt 1" });
    expect(session.getAttribute("href")).toBe(`/runs/${RUN.claudeCode}/trials/rn-basic__sryqofs`);
    expect(within(grid).getByText("0.36")).toBeTruthy();
    expect(within(grid).getByRole("img", { name: "did not pass" })).toBeTruthy();
    expect(within(trials).getByRole("link", { name: "Compare with and without" }).getAttribute("href")).toBe(
      `/runs/${RUN.claudeCode}/cases/rn-basic/compare`,
    );
    expect(within(trials).getAllByText("ANTHROPIC_API_KEY", { selector: "code" })[0]?.closest("dd")?.textContent).toContain("api.anthropic.com");
    expect(within(trials).getByText(/Network flows: off/)).toBeTruthy();
  });

  it("shows attempts, the retry, a withheld cell and the dropped flows", async () => {
    renderAt(`/runs/${RUN.codex}`);
    const attempts = await screen.findByRole("region", { name: "Attempts" });
    expect(within(attempts).getByText("Attempt 2").getAttribute("aria-current")).toBe("page");
    expect(within(attempts).getByRole("link", { name: "Attempt 1" }).getAttribute("href")).toBe(
      `/runs/${RUN.codex}?job=acme-release-notes-v1-1-0-codex-0-161-0-openai-gpt-6-1-sol`,
    );
    expect(screen.getByText(/^Retried once after an infrastructure failure: EnvironmentStartTimeoutError/)).toBeTruthy();
    const grid = screen.getByRole("table", { name: /Trials of case rn-basic/ });
    expect(within(grid).getByText("withheld")).toBeTruthy();
    expect(within(grid).getByText(/secret-scan: the secret scan found a value/)).toBeTruthy();
    expect(within(grid).getByText(/1 dropped/)).toBeTruthy();
    expect(screen.getByText("The report files are stored; this run has no dimension scores.")).toBeTruthy();
  });

  it("marks an earlier attempt and keeps ?job= on the session links", async () => {
    const job = "acme-release-notes-v1-1-0-codex-0-161-0-openai-gpt-6-1-sol";
    renderAt(`/runs/${RUN.codex}?job=${job}`);
    expect(await screen.findByText(/You are looking at attempt 1/)).toBeTruthy();
    const link = screen.getByRole("link", { name: /^Session rn-basic__Cx7aQ1p/ });
    expect(link.getAttribute("href")).toBe(`/runs/${RUN.codex}/trials/rn-basic__Cx7aQ1p?job=${job}`);
  });

  it("falls back to result.json scores without session links", async () => {
    renderAt(`/runs/${RUN.fallback}`);
    expect(await screen.findByText(/This run has no trial bundle/)).toBeTruthy();
    const grid = screen.getByRole("table", { name: /Trials of case rn-basic/ });
    expect(within(grid).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Arm", "Attempt 1", "Attempt 2", "Attempt 3"]);
    expect(within(grid).queryAllByRole("link")).toHaveLength(0);
    expect(screen.queryByRole("link", { name: /Compare/ })).toBeNull();
  });

  it("shows unscored trials and the run's failure", async () => {
    renderAt(`/runs/${RUN.unscored}`);
    expect(await screen.findByText(/^no scores in result.json/)).toBeTruthy();
    const grid = screen.getByRole("table", { name: /Trials of case rn-basic/ });
    expect(within(grid).getAllByText("unscored RuntimeError")).toHaveLength(2);
    expect(within(grid).getAllByRole("link")).toHaveLength(2);
    expect(screen.getByText("tier3/execution: Harbor job did not complete successfully: 1 errored")).toBeTruthy();
  });
});
