// Hand-built SVG charts. Rules (the dataviz method): color by job (with-skill
// and without-skill are categorical slots 1 and 2, the phases a one-hue ordinal
// ramp, a lift one series), one axis, thin marks with rounded data ends, a 2px
// surface gap between touching fills, a surface ring on points, hairline solid
// gridlines, a legend for two series, and the numbers always in text next to or
// under the chart (the table view), so a tooltip never gates a value. Each mark
// has an SVG <title> for the pointer tooltip. Colors come from tokens.css.
import type { Dimension, Interval, PhaseTiming } from "../api/types.ts";
import { duration, interval as ciText, score, signed } from "../format.ts";

/** barPath is a horizontal bar from x0 to x1, square at the baseline and rounded (up to 4px) at the data end. */
export function barPath(x0: number, x1: number, y: number, h: number): string {
  const w = Math.max(0, x1 - x0);
  const r = Math.min(4, h / 2, w);
  return [
    `M${x0},${y}`,
    `H${x1 - r}`,
    `A${r},${r} 0 0 1 ${x1},${y + r}`,
    `V${y + h - r}`,
    `A${r},${r} 0 0 1 ${x1 - r},${y + h}`,
    `H${x0}`,
    "Z",
  ].join(" ");
}

const LIFT_W = 88;
const LIFT_H = 14;
const LIFT_PAD = 5;

/** liftX maps a lift in [-1, 1] onto the inline bar. */
export function liftX(v: number): number {
  const c = Math.max(-1, Math.min(1, v));
  return LIFT_PAD + ((c + 1) / 2) * (LIFT_W - 2 * LIFT_PAD);
}

/**
 * LiftBar is the inline interval chart next to a lift: a hairline at zero, the
 * confidence interval as a thin band, the lift as a point. The scale is fixed
 * at -1 to +1 everywhere, so bars in one table compare. The text beside it
 * carries the numbers, so the SVG is hidden from assistive technology.
 */
export function LiftBar({ lift, ci }: { lift: number; ci?: Interval }) {
  const zero = liftX(0);
  const tip = ci ? `Lift ${signed(lift)}, ${ciText(ci)}` : `Lift ${signed(lift)}, no interval`;
  return (
    <svg className="viz viz-lift" width={LIFT_W} height={LIFT_H} viewBox={`0 0 ${LIFT_W} ${LIFT_H}`} aria-hidden="true">
      <title>{tip}</title>
      <line className="viz-baseline" x1={LIFT_PAD} x2={LIFT_W - LIFT_PAD} y1={LIFT_H / 2} y2={LIFT_H / 2} />
      <line className="viz-zero" x1={zero} x2={zero} y1={1} y2={LIFT_H - 1} />
      {ci ? (
        <rect
          className="viz-interval"
          x={liftX(ci.lower)}
          y={LIFT_H / 2 - 2}
          width={Math.max(2, liftX(ci.upper) - liftX(ci.lower))}
          height={4}
          rx={2}
        />
      ) : null}
      <circle className="viz-point" cx={liftX(lift)} cy={LIFT_H / 2} r={4} />
    </svg>
  );
}

/** Legend for the two arms; identity never relies on color matching alone. */
export function ArmLegend() {
  return (
    <ul className="viz-legend" aria-label="Legend">
      <li>
        <svg width="12" height="8" aria-hidden="true">
          <rect className="viz-with" width="12" height="8" rx="2" />
        </svg>
        With skill
      </li>
      <li>
        <svg width="12" height="8" aria-hidden="true">
          <rect className="viz-without" width="12" height="8" rx="2" />
        </svg>
        Without skill
      </li>
    </ul>
  );
}

const DIM_W = 160;
const DIM_BAR = 7;
const DIM_GAP = 2;

/** DimensionBars is one row's pair of bars (with skill above, without below) on a 0 to 1 scale. */
export function DimensionBars({ d }: { d: Dimension }) {
  const h = DIM_BAR * 2 + DIM_GAP;
  const x = (v: number) => Math.max(0, Math.min(1, v)) * DIM_W;
  return (
    <svg className="viz viz-dim" width={DIM_W} height={h} viewBox={`0 0 ${DIM_W} ${h}`} role="img" aria-label={`${d.label}: with skill ${score(d.withSkill)}, without skill ${score(d.withoutSkill)}`}>
      {[0.5, 1].map((t) => (
        <line key={t} className="viz-grid" x1={x(t) - 0.5} x2={x(t) - 0.5} y1={0} y2={h} />
      ))}
      <path className="viz-with" d={barPath(0, Math.max(x(d.withSkill), 1), 0, DIM_BAR)}>
        <title>{`${d.label}, with skill: ${score(d.withSkill)}`}</title>
      </path>
      <path className="viz-without" d={barPath(0, Math.max(x(d.withoutSkill), 1), DIM_BAR + DIM_GAP, DIM_BAR)}>
        <title>{`${d.label}, without skill: ${score(d.withoutSkill)}`}</title>
      </path>
      <line className="viz-baseline" x1={0.5} x2={0.5} y1={0} y2={h} />
    </svg>
  );
}

/**
 * DimensionTable is the six SkillEvaluator dimensions, with skill against
 * without: the table is the data, the bars in the last column show it.
 */
export function DimensionTable({ dims, caption }: { dims: Dimension[]; caption: string }) {
  return (
    <figure className="figure">
      <figcaption className="figure-head">
        <span>{caption}</span>
        <ArmLegend />
      </figcaption>
      <div className="table-wrap">
        <table className="table table-dims">
          <thead>
            <tr>
              <th scope="col">Dimension</th>
              <th scope="col" className="num">
                With
              </th>
              <th scope="col" className="num">
                Without
              </th>
              <th scope="col" className="num">
                Delta
              </th>
              <th scope="col">
                <span className="sr-only">Bars, 0 to 1</span>
                <span aria-hidden="true" className="axis-labels">
                  <span>0</span>
                  <span>0.5</span>
                  <span>1</span>
                </span>
              </th>
            </tr>
          </thead>
          <tbody>
            {dims.map((d) => (
              <tr key={d.key} className={d.overall ? "row-strong" : undefined}>
                <th scope="row">{d.label}</th>
                <td className="num">{score(d.withSkill)}</td>
                <td className="num">{score(d.withoutSkill)}</td>
                <td className="num">{signed(d.delta)}</td>
                <td>
                  <DimensionBars d={d} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </figure>
  );
}

const PHASE_W = 480;
const PHASE_H = 8;
const PHASE_GAP = 2;

/**
 * PhaseBar is a trial's time split into its phases (environment setup, agent
 * setup, agent execution, verifier): a stacked bar on a one-hue ordinal ramp,
 * with the phases and their durations listed under it.
 */
export function PhaseBar({ phases }: { phases: PhaseTiming[] }) {
  if (phases.length === 0) return null;
  const total = phases.reduce((s, p) => s + Math.max(0, p.durationSec), 0) || 1;
  const usable = PHASE_W - PHASE_GAP * (phases.length - 1);
  let x = 0;
  const segs = phases.map((p, i) => {
    const w = Math.max(2, (Math.max(0, p.durationSec) / total) * usable);
    const seg = { p, i, x, w };
    x += w + PHASE_GAP;
    return seg;
  });
  const scale = PHASE_W / Math.max(PHASE_W, x - PHASE_GAP);
  const label = phases.map((p) => `${p.label} ${duration(p.durationSec)}`).join(", ");
  return (
    <figure className="figure phase-figure">
      <svg className="viz viz-phase" viewBox={`0 0 ${PHASE_W} ${PHASE_H}`} preserveAspectRatio="xMinYMid meet" role="img" aria-label={`Phases: ${label}`}>
        {segs.map(({ p, i, x: sx, w }) => {
          const first = i === 0;
          const last = i === segs.length - 1;
          const x0 = sx * scale;
          const x1 = (sx + w) * scale;
          const d = first && last ? roundBoth(x0, x1) : first ? roundLeft(x0, x1) : last ? barPath(x0, x1, 0, PHASE_H) : rectPath(x0, x1);
          return (
            <path key={p.name} className={`viz-phase-${Math.min(4, i + 1)}`} d={d}>
              <title>{`${p.label}: ${duration(p.durationSec)}`}</title>
            </path>
          );
        })}
      </svg>
      <figcaption>
        <ol className="phase-legend">
          {phases.map((p, i) => (
            <li key={p.name}>
              <svg width="10" height="8" aria-hidden="true">
                <rect className={`viz-phase-${Math.min(4, i + 1)}`} width="10" height="8" rx="2" />
              </svg>
              <span className="label">{p.label}</span> <span className="num">{duration(p.durationSec)}</span>
            </li>
          ))}
        </ol>
      </figcaption>
    </figure>
  );
}

function rectPath(x0: number, x1: number): string {
  return `M${x0},0 H${x1} V${PHASE_H} H${x0} Z`;
}

function roundLeft(x0: number, x1: number): string {
  const r = Math.min(4, PHASE_H / 2, x1 - x0);
  return `M${x1},0 V${PHASE_H} H${x0 + r} A${r},${r} 0 0 1 ${x0},${PHASE_H - r} V${r} A${r},${r} 0 0 1 ${x0 + r},0 Z`;
}

function roundBoth(x0: number, x1: number): string {
  const r = Math.min(4, PHASE_H / 2, (x1 - x0) / 2);
  return `M${x0 + r},0 H${x1 - r} A${r},${r} 0 0 1 ${x1},${r} V${PHASE_H - r} A${r},${r} 0 0 1 ${x1 - r},${PHASE_H} H${x0 + r} A${r},${r} 0 0 1 ${x0},${PHASE_H - r} V${r} A${r},${r} 0 0 1 ${x0 + r},0 Z`;
}

const METER_W = 44;
const METER_H = 6;

/** ScoreMeter is a 0 to 1 score as a small meter; the number is always written next to it. */
export function ScoreMeter({ value, mean }: { value: number; mean?: number }) {
  const x = (v: number) => Math.max(0, Math.min(1, v)) * METER_W;
  return (
    <svg className="viz viz-meter" width={METER_W} height={METER_H + 4} viewBox={`0 0 ${METER_W} ${METER_H + 4}`} aria-hidden="true">
      <title>{mean === undefined ? `Score ${score(value)}` : `Score ${score(value)}, arm mean ${score(mean)}`}</title>
      <rect className="viz-track" x={0} y={2} width={METER_W} height={METER_H} rx={3} />
      {value > 0 ? <path className="viz-fill" d={barPath(0, Math.max(x(value), 2), 2, METER_H)} /> : null}
      {mean !== undefined ? <line className="viz-mean" x1={x(mean)} x2={x(mean)} y1={0} y2={METER_H + 4} /> : null}
    </svg>
  );
}
