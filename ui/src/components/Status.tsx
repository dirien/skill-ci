// Status vocabulary: one pill or badge per state, used on every page. A state
// is always an icon and a word; the color only repeats what they say.
import type { ReactNode } from "react";

import type { Phase, RunSummary, TierStatus, Tiers, Verdict } from "../api/types.ts";
import { cap } from "../format.ts";

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

/** PHASE is the tone and icon of each run phase, the same on every page. */
export const PHASE: Record<Phase, [Tone, string]> = {
  Pending: ["neutral", "○"],
  Planned: ["neutral", "◌"],
  Running: ["info", "◐"],
  Succeeded: ["ok", "✓"],
  Failed: ["bad", "✕"],
};

export function phaseLook(phase: Phase): [Tone, string] {
  return PHASE[phase] ?? ["neutral", "○"];
}

export function PhasePill({ phase }: { phase: Phase }) {
  const [tone, icon] = phaseLook(phase);
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
        Passed
      </Pill>
    );
  if (value === "failed")
    return (
      <Pill tone="bad" icon="✕">
        Failed
      </Pill>
    );
  return (
    <Pill tone="neutral" icon="●">
      {cap(value)}
    </Pill>
  );
}

/** WITHHELD_SHORT names a skillci.dev/trials-withheld reason in two or three words. */
const WITHHELD_SHORT: Record<string, [string, string]> = {
  "secret-scan": ["secret found", "The secret scan found a value after redaction, so no trial was stored."],
  size: ["too large", "The bundle was over its size limit."],
  "build-failed": ["build failed", "The bundle could not be built."],
  "attach-failed": ["upload failed", "The bundle could not be attached to the registry."],
};

/** trialsNote is the short reason a run has no sessions, and its longer explanation. */
export function trialsNote(r: Pick<RunSummary, "trials" | "trialsReason" | "hasReport" | "phase" | "tiers">): [string, string] {
  if (r.trials === "withheld") {
    const known = r.trialsReason ? WITHHELD_SHORT[r.trialsReason] : undefined;
    return known ?? [r.trialsReason ?? "no reason", `The runner withheld the trial bundle${r.trialsReason ? ` (${r.trialsReason})` : ""}.`];
  }
  if (r.trials === "unknown") return ["registry slow", "The registry did not answer in time; open the run to see its trials."];
  if (r.phase === "Pending" || r.phase === "Running") return ["not yet", "Sessions are stored when the run finishes."];
  if (r.phase === "Planned") return ["dry run", "A dry run starts no trials."];
  if (!r.hasReport) return ["no report", "The run stored no report, so there are no sessions."];
  if (r.tiers.tier3 === "skipped") return ["Tier 3 not run", "Tier 1 or 2 rejected this version, so no live trials ran."];
  if (r.trialsReason === "disabled") return ["bundles off", "Trial bundles were turned off for this run (TRIAL_BUNDLE=off)."];
  if (r.trialsReason === "no-trials") return ["no trials", "Tier 3 produced no trials to store."];
  return ["older run", "This run is older than trial bundles: scores only, no sessions."];
}

/** TrialsText says whether a run has trial sessions and, when not, why in two or three words. */
export function TrialsText({ run }: { run: Pick<RunSummary, "trials" | "trialsReason" | "hasReport" | "phase" | "tiers"> }) {
  if (run.trials === "stored")
    return (
      <Pill tone="info" icon="≡">
        Sessions
      </Pill>
    );
  const [short, long] = trialsNote(run);
  return (
    <span className="trials-note" title={long}>
      {run.trials === "withheld" ? (
        <Pill tone="warn" icon="!">
          Withheld
        </Pill>
      ) : (
        <span className="muted">None</span>
      )}
      <span className="cell-sub">{short}</span>
      <span className="sr-only">. {long}</span>
    </span>
  );
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
