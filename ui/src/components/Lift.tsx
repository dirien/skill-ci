// Lift with its evidence (CONTRACT.md, "Reporting site"): "+0.59" with
// "95% CI +0.41 to +0.73, n=9/9" and the inline interval bar. A run without
// trial scores shows a dash.
import type { Interval } from "../api/types.ts";
import { interval, signed } from "../format.ts";
import { LiftBar } from "./Charts.tsx";

export function Lift({
  lift,
  ci,
  n,
  compact = false,
  evidence: showEvidence = true,
}: {
  lift?: number;
  ci?: Interval;
  n?: string;
  compact?: boolean;
  /** evidence false leaves out the interval line, for a mean over runs. */
  evidence?: boolean;
}) {
  if (lift === undefined) return <span className="muted">-</span>;
  const evidence = ci ? `${interval(ci)}${n ? `, n=${n}` : ""}` : n ? `n=${n}, no interval` : "no interval";
  return (
    <span className={compact ? "lift lift-compact" : "lift"}>
      <span className="lift-row">
        <strong className="num">{signed(lift)}</strong>
        <LiftBar lift={lift} ci={ci} />
      </span>
      {showEvidence ? <span className="lift-ci">{evidence}</span> : null}
    </span>
  );
}
