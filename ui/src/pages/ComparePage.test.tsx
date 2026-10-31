import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { RUN } from "../api/fixtures/index.ts";
import { renderAt } from "../test/render.tsx";

describe("compare page", () => {
  it("puts both arms of a case side by side", async () => {
    renderAt(`/runs/${RUN.claudeCode}/cases/rn-basic/compare`);
    const table = await screen.findByRole("table", { name: "The two trials side by side" });
    const score = within(table).getByRole("rowheader", { name: "Score" }).closest("tr") as HTMLElement;
    expect(score.textContent).toBe("Score0.950.36");
    const crit = within(table).getByRole("rowheader", { name: "Criteria" }).closest("tr") as HTMLElement;
    expect(crit.textContent).toBe("Criteria4 of 41 of 4");

    const withSide = screen.getByRole("region", { name: "With skill" });
    expect(within(withSide).getByRole("link", { name: "Open session rn-basic__sryqofs" }).getAttribute("href")).toBe(
      `/runs/${RUN.claudeCode}/trials/rn-basic__sryqofs`,
    );
    const without = screen.getByRole("region", { name: "Without skill" });
    expect(within(without).getAllByText("Failed").length).toBe(3);
    expect(within(without).getByRole("list", { name: "Without skill log" })).toBeTruthy();
  });

  it("says when one arm has no trial", async () => {
    renderAt(`/runs/${RUN.codex}/cases/rn-basic/compare`);
    const without = await screen.findByRole("region", { name: "Without skill" });
    expect(within(without).getByText("No without-skill trial for attempt 1 in the bundle.")).toBeTruthy();
  });
});
