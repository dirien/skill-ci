import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { RUN } from "../api/fixtures/index.ts";
import type { TrialSession } from "../api/types.ts";
import { renderAt, stubApi } from "../test/render.tsx";

const WITH = `/runs/${RUN.claudeCode}/trials/rn-basic__sryqofs`;
const CODEX = `/runs/${RUN.codex}/trials/rn-basic__Cx7aQ1p`;

function entries(): HTMLElement[] {
  return within(screen.getByRole("list", { name: "Session log" })).getAllByRole("listitem").filter((li) => li.parentElement?.getAttribute("aria-label") === "Session log");
}

describe("trial session", () => {
  it("shows the header, the prompt and the metadata strip", async () => {
    const { container } = renderAt(WITH);
    expect(await screen.findByRole("heading", { level: 1, name: "rn-basic__sryqofs" })).toBeTruthy();
    expect(screen.getByText(/^Write the release notes for version 1.4.0/, { selector: "pre.prompt-text" })).toBeTruthy();
    const meta = container.querySelector('dl[aria-label="Trial metadata"]') as HTMLElement;
    const text = meta.textContent ?? "";
    for (const part of ["Armwith skill", "Attempt1 of 1", "claude-code@2.1.292", "Workspace/workspace", "invoked", "Pushed byrelease-bot", "4 of 4 passed", "ATIF-v1.7"]) {
      expect(text).toContain(part);
    }
    expect(text).toContain("ANTHROPIC_API_KEY via api.anthropic.com");
    expect(screen.getByRole("img", { name: /^Phases: Environment setup 4m 26s/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: "trajectory.json" }).getAttribute("href")).toContain("/trajectory.json");
  });

  it("filters the log with chips, Focus and All", async () => {
    renderAt(WITH);
    await screen.findByRole("list", { name: "Session log" });
    const tools = screen.getByRole("checkbox", { name: "tools, 1 entry" });
    const usage = screen.getByRole("checkbox", { name: "usage, 3 entries" });
    expect((tools as HTMLInputElement).checked).toBe(true);
    expect((usage as HTMLInputElement).checked).toBe(false);
    expect(entries()).toHaveLength(11);
    expect(screen.getByRole("button", { name: "Focus" }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(screen.getByRole("button", { name: "All" }));
    expect(entries()).toHaveLength(17);
    expect(screen.getByRole("button", { name: "All" }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(screen.getByRole("checkbox", { name: "judge, 6 entries" }));
    expect(entries()).toHaveLength(11);
    expect(screen.getByRole("button", { name: "All" }).getAttribute("aria-pressed")).toBe("false");
    expect(screen.getByRole("button", { name: "Focus" }).getAttribute("aria-pressed")).toBe("false");
  });

  it("renders tool calls as commands with output, a check or a cross", async () => {
    renderAt(CODEX);
    const pip = await screen.findByRole("listitem", { name: "Step 3, exec_command package install, failed" });
    expect(within(pip).getByText("$ pip install markdown-it-py")).toBeTruthy();
    expect(within(pip).getByRole("img", { name: "failed" })).toBeTruthy();
    const output = within(pip).getByText(/^output/).closest("details") as HTMLDetailsElement;
    expect(output.open).toBe(true);
    expect(within(pip).getByRole("link", { name: "Full output as text" }).getAttribute("href")).toBe(
      `/api/v1${CODEX}/steps/3/output/0`,
    );
    const dropped = screen.getByRole("listitem", { name: "Network, DROPPED pypi.org port 443" });
    expect(dropped.textContent).toContain("POLICY_DENIED");
    expect(screen.getByRole("link", { name: "First error" }).getAttribute("href")).toBe(`${CODEX}#s-3-2`);
  });

  it("moves between entries with j, k, g g and G, and / focuses the chips", async () => {
    renderAt(WITH);
    await screen.findByRole("list", { name: "Session log" });
    // Let the top bar's site request land first, so no render happens between key presses.
    await screen.findByText("skill-ci", { selector: "code" });
    const list = entries();
    fireEvent.keyDown(document, { key: "j" });
    expect(document.activeElement).toBe(list[0]);
    fireEvent.keyDown(document, { key: "j" });
    expect(document.activeElement).toBe(list[1]);
    fireEvent.keyDown(document, { key: "k" });
    expect(document.activeElement).toBe(list[0]);
    fireEvent.keyDown(document, { key: "G" });
    expect(document.activeElement).toBe(list[list.length - 1]);
    fireEvent.keyDown(document, { key: "g" });
    fireEvent.keyDown(document, { key: "g" });
    expect(document.activeElement).toBe(list[0]);
    fireEvent.keyDown(document, { key: "/" });
    expect(document.activeElement).toBe(screen.getByRole("checkbox", { name: "msg, 1 entry" }));
    // Shortcuts never fire while typing or with a modifier.
    fireEvent.keyDown(document, { key: "j", ctrlKey: true });
    expect(document.activeElement).toBe(screen.getByRole("checkbox", { name: "msg, 1 entry" }));
  });

  it("jumps to a step from the criteria and shows a hidden entry", async () => {
    const { router } = renderAt(`${WITH}#s-2-2`);
    await screen.findByRole("list", { name: "Session log" });
    // s-2-2 is a usage entry, hidden in Focus: the jump turns usage on and focuses it.
    await waitFor(() => expect(document.activeElement?.id).toBe("s-2-2"));
    expect(screen.getByRole<HTMLInputElement>("checkbox", { name: "usage, 3 entries" }).checked).toBe(true);

    const criteria = screen.getByRole("region", { name: "Eval criteria" });
    expect(within(criteria).getByText("4 of 4 passed")).toBeTruthy();
    expect(within(criteria).getAllByText("Passed").length).toBeGreaterThanOrEqual(4);
    const chip = within(within(criteria).getByRole("list", { name: "Steps the behavior check cites" })).getByRole("link", { name: "Step 4" });
    act(() => {
      fireEvent.click(chip);
    });
    await waitFor(() => expect(router.state.location.hash).toBe("#s-4"));
    await waitFor(() => expect(document.activeElement?.id).toBe("s-4"));
    expect(document.getElementById("s-4")?.className).toContain("is-target");
  });

  it("shows typed session findings with their rule", async () => {
    renderAt(CODEX);
    const findings = await screen.findByRole("region", { name: "Session findings" });
    expect(within(findings).getAllByText("Friction")).toHaveLength(4);
    const drop = within(findings).getByText("DROPPED pypi.org:443").closest("li") as HTMLElement;
    expect(within(drop).getByRole("link", { name: "Step 3" }).getAttribute("href")).toBe(`${CODEX}#s-3`);
  });

  it("shows the files the agent wrote, with a link to the step", async () => {
    renderAt(`/runs/${RUN.opencode}/trials/rn-basic__Op4mZ8e/files`);
    const files = await screen.findByRole("region", { name: "Files" });
    const articles = within(files).getAllByRole("article");
    expect(articles).toHaveLength(2);
    expect(articles[0]?.textContent).toContain("- Fixed a login timeout (ACME-117)");
    expect(within(articles[1] as HTMLElement).getByText("- Fixed a login timeout [ACME-117]")).toBeTruthy();
    expect(within(articles[1] as HTMLElement).getByRole("link", { name: "Step 4" }).getAttribute("href")).toBe(
      `/runs/${RUN.opencode}/trials/rn-basic__Op4mZ8e#s-4-2`,
    );
    expect(screen.getByRole("link", { name: /^Files/ }).getAttribute("aria-current")).toBe("page");
  });

  it("marks a shortened output", async () => {
    renderAt(`/runs/${RUN.opencode}/trials/rn-basic__Op4mZ8e`);
    const cat = await screen.findByRole("listitem", { name: /^Step 5, bash/ });
    expect(within(cat).getByText(/shortened in the bundle/)).toBeTruthy();
  });

  it("shows the judge metrics and their stored details", async () => {
    renderAt(`${WITH}/judge`);
    const judge = await screen.findByRole("region", { name: "Judge" });
    expect(within(judge).getAllByRole("article")).toHaveLength(6);
    expect(within(judge).getByRole("heading", { name: "Goal accuracy" })).toBeTruthy();
    expect(within(judge).getAllByText("Details as stored in reward.json").length).toBeGreaterThan(0);
  });

  it("shows flows and DNS names on the network tab", async () => {
    renderAt(`${CODEX}/network`);
    const net = await screen.findByRole("region", { name: "Network" });
    const flows = within(net).getByRole("table", { name: "Connections of this trial" });
    const row = within(flows).getByText("pypi.org").closest("tr") as HTMLElement;
    expect(within(row).getByText("DROPPED")).toBeTruthy();
    expect(within(row).getByText("POLICY_DENIED")).toBeTruthy();
    expect(within(net).getByRole("table", { name: "DNS names the trial asked for" })).toBeTruthy();
  });

  it("says why there are no flows", async () => {
    renderAt(`/runs/${RUN.opencode}/trials/rn-basic__Op4mZ8e/network`);
    expect(await screen.findByText(/did not answer within 10s/)).toBeTruthy();
  });

  it("shows an unscored trial with its error and no log", async () => {
    renderAt(`/runs/${RUN.unscored}/trials/rn-basic__CbK6FQ2`);
    expect(await screen.findByText("RuntimeError", { selector: "strong" })).toBeTruthy();
    expect(screen.getByText(/This trial has no trajectory/)).toBeTruthy();
    expect(screen.getByRole("region", { name: "Eval criteria" }).textContent).toContain("Not judged");
  });

  it("renders agent output as text, never as HTML", async () => {
    stubApi(`/runs/${RUN.claudeCode}/trials/rn-basic__sryqofs`, (body) => {
      const s = body as TrialSession;
      const first = s.log.entries[0];
      if (first) first.text = '<img src=x onerror="alert(1)"><script>alert(2)</script>';
      s.header.prompt = "<b>bold?</b>";
      return s;
    });
    const { container } = renderAt(WITH);
    await screen.findByRole("list", { name: "Session log" });
    expect(container.querySelector("img, script, b")).toBeNull();
    expect(screen.getByText("<b>bold?</b>")).toBeTruthy();
  });

  it("answers an unknown trial with the API's 404", async () => {
    renderAt(`/runs/${RUN.claudeCode}/trials/nope`);
    expect((await screen.findByRole("alert")).textContent).toContain("Not found");
  });
});
