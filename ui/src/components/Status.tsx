// Status vocabulary: one pill or badge per state, used on every page. A state
// is always an icon and a word; the color only repeats what they say.
import type { ReactNode } from "react";

import type { Phase, TierStatus, Tiers, TrialsState, Verdict } from "../api/types.ts";

export type Tone = "ok" | "bad" | "warn" | "info" | "neutral" | "opp";

/** Pill is a rounded status label: icon, then the word. */
export function Pill({ tone, icon, children, title }: { tone: Tone; icon: string; children: ReactNode; title?: string }) {
  return (
    <span className={`pill tone-${tone}`} title={title}>
      <span className="pill-icon" aria-hidden="true">
        {icon}
      </span>
      {children}
    </span>
  );
}

/** Badge is a square, uppercase label for typed items (PASSED, FRICTION). */
export function Badge({ tone, icon, children }: { tone: Tone; icon?: string; children: ReactNode }) {
  return (
    <span className={`badge tone-${tone}`}>
      {icon ? (
        <span className="badge-icon" aria-hidden="true">
          {icon}
        </span>
      ) : null}
      {children}
    </span>
  );
}

const PHASE: Record<Phase, [Tone, string]> = {
  Pending: ["neutral", "○"],
  Planned: ["neutral", "◌"],
  Running: ["info", "◐"],
  Succeeded: ["ok", "✓"],
  Failed: ["bad", "✕"],
};

export function PhasePill({ phase }: { phase: Phase }) {
  const [tone, icon] = PHASE[phase] ?? ["neutral", "○"];
  return (
    <Pill tone={tone} icon={icon}>
      {phase}
    </Pill>
  );
}

const VERDICT: Record<Verdict, [Tone, string]> = {
  Helps: ["ok", "▲"],
  Neutral: ["neutral", "●"],
  Hurts: ["bad", "▼"],
  Obsolete: ["neutral", "◌"],
  Regressed: ["bad", "▼"],
  Inconclusive: ["neutral", "○"],
  Rejected: ["bad", "✕"],
};

export function VerdictPill({ verdict }: { verdict?: Verdict }) {
  if (!verdict) return <span className="muted">-</span>;
  const [tone, icon] = VERDICT[verdict] ?? ["neutral", "●"];
  return (
    <Pill tone={tone} icon={icon}>
      {verdict}
    </Pill>
  );
}

const TIER: Record<TierStatus, [Tone, string]> = {
  passed: ["ok", "✓"],
  failed: ["bad", "✕"],
  incomplete: ["warn", "!"],
  skipped: ["neutral", "-"],
};

/** TierMarkers writes T1 T2 T3 with one mark each; a tier that did not run yet is "·". */
export function TierMarkers({ tiers }: { tiers: Tiers }) {
  const all: [string, TierStatus | undefined][] = [
    ["1", tiers.tier1],
    ["2", tiers.tier2],
    ["3", tiers.tier3],
  ];
  const label = all.map(([n, s]) => `Tier ${n} ${s ?? "not run"}`).join(", ");
  return (
    <span className="tiers" role="img" aria-label={label} title={label}>
      {all.map(([n, s]) => {
        const [tone, icon] = s ? TIER[s] : (["neutral", "·"] as [Tone, string]);
        return (
          <span key={n} className={`tier tone-${tone}`} aria-hidden="true">
            T{n}
            <span className="tier-icon">{icon}</span>
          </span>
        );
      })}
    </span>
  );
}

/** PublicationPill is the Tier 2 publication gate result. */
export function PublicationPill({ value }: { value?: string }) {
  if (!value) return <span className="muted">-</span>;
  if (value === "passed")
    return (
      <Pill tone="ok" icon="✓">
        passed
      </Pill>
    );
  if (value === "failed")
    return (
      <Pill tone="bad" icon="✕">
        failed
      </Pill>
    );
  return (
    <Pill tone="neutral" icon="●">
      {value}
    </Pill>
  );
}

/** TrialsText says whether a run has trial sessions. */
export function TrialsText({ state }: { state: TrialsState }) {
  switch (state) {
    case "stored":
      return (
        <Pill tone="info" icon="≡">
          sessions
        </Pill>
      );
    case "withheld":
      return (
        <Pill tone="warn" icon="!">
          withheld
        </Pill>
      );
    case "unknown":
      return <span className="muted">unknown</span>;
    default:
      return <span className="muted">none</span>;
  }
}

/** PassBadge is PASSED, FAILED or NOT JUDGED for a criterion. */
export function PassBadge({ passed }: { passed?: boolean }) {
  if (passed === undefined)
    return (
      <Badge tone="neutral" icon="○">
        Not judged
      </Badge>
    );
  return passed ? (
    <Badge tone="ok" icon="✓">
      Passed
    </Badge>
  ) : (
    <Badge tone="bad" icon="✕">
      Failed
    </Badge>
  );
}

/** Mark is a check or cross with an accessible word, for tool calls and table cells. */
export function Mark({ ok, okText = "succeeded", badText = "failed" }: { ok: boolean; okText?: string; badText?: string }) {
  return (
    <span className={`mark tone-${ok ? "ok" : "bad"}`} role="img" aria-label={ok ? okText : badText} title={ok ? okText : badText}>
      {ok ? "✓" : "✕"}
    </span>
  );
}
