import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { RUN } from "../api/fixtures/index.ts";
import { renderAt } from "../test/render.tsx";

describe("overview page", () => {
  it("shows counts, needs attention, recent runs, channels and the network policy", async () => {
    renderAt("/");
    const failed = await screen.findByText("Failed", { selector: "dt" });
    expect(failed.nextElementSibling?.textContent).toBe("1");
    // Pending has its own tile, and each phase tile opens the runs in that phase.
    const pending = screen.getByRole("link", { name: "1 pending run" });
    expect(pending.getAttribute("href")).toBe("/runs?phase=Pending");
    expect(screen.getByRole("link", { name: "1 running run" }).getAttribute("href")).toBe("/runs?phase=Running");

    const attention = screen.getByRole("region", { name: "Needs attention" });
    const items = within(attention).getAllByRole("listitem");
    expect(items).toHaveLength(4);
    // The pill says the run's phase, as the runs table does.
    expect(items[2]?.textContent).toContain("Running");
    expect(items[3]?.textContent).toContain("Pending");
    expect(items[3]?.textContent).toContain("Waiting for a free slot (1 of 1 jobs running).");
    expect(within(attention).getByText(/^No scores in result.json/)).toBeTruthy();
    expect(within(attention).getByText(/Rejected by Tier 1: tier1\/security/)).toBeTruthy();

    const recent = screen.getByRole("region", { name: "Recent runs" });
    const rows = within(recent).getAllByRole("row");
    expect(rows).toHaveLength(9);
    const link = within(recent).getAllByRole("link").find((a) => a.getAttribute("href") === `/runs/${RUN.claudeCode}`);
    expect(link?.textContent).toBe("acme-release-notes v1.1.0");
    // The one run with an interval shows it in words next to the bar.
    expect(within(recent).getByText("95% CI +0.51 to +0.65, n=3/3")).toBeTruthy();
    // Status is never color alone: pills carry a word.
    expect(within(recent).getAllByText("Succeeded").length).toBeGreaterThan(0);
    expect(within(recent).getByRole("img", { name: "Tier 1 failed, Tier 2 passed, Tier 3 skipped" })).toBeTruthy();

    const channels = screen.getByRole("region", { name: "Release channels" });
    expect(within(channels).getByRole("rowheader", { name: "python-docstrings" }).closest("tr")?.textContent).toContain("v1.1.0");

    const policy = screen.getByRole("region", { name: "Network policy" });
    expect(within(policy).getByText("hubble-relay.kube-system.svc.cluster.local:80")).toBeTruthy();
  });
});
