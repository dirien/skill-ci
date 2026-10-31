// The right column of the Log tab: session findings and eval criteria. Step
// chips jump to the log entry of that ATIF step.
import type { Criteria, Finding } from "../api/types.ts";
import { Empty } from "../components/Page.tsx";
import { Badge, PassBadge } from "../components/Status.tsx";
import { score } from "../format.ts";
import { StepChips } from "./Entry.tsx";

const RULES: Record<string, string> = {
  "metric-failed": "a judge metric failed",
  "metric-low": "a judge metric scored below 0.8",
  "check-failed": "a sub-check failed",
  exception: "the trial raised an exception",
  "flow-dropped": "the network policy dropped a connection",
  "eval-error": "the judge reported an evaluation error",
  "skill-not-used": "the skill was installed but not used",
  suggestion: "a run-level suggestion of SkillEvaluator",
};

export function FindingsPanel({ findings }: { findings: Finding[] }) {
  return (
    <section className="panel" id="findings" aria-labelledby="findings-h">
      <header className="panel-head">
        <h2 id="findings-h">Session findings</h2>
        <span className="muted">{findings.length}</span>
      </header>
      {findings.length === 0 ? (
        <Empty>No findings for this trial.</Empty>
      ) : (
        <ul className="finding-list">
          {findings.map((f, i) => (
            <li key={`${f.rule}-${f.metric ?? ""}-${i}`} className="finding">
              <div className="finding-head">
                {f.kind === "friction" ? (
                  <Badge tone="warn" icon="▲">
                    Friction
                  </Badge>
                ) : (
                  <Badge tone="opp" icon="◆">
                    Opportunity
                  </Badge>
                )}
                <span className="finding-title">{f.title}</span>
                {f.runLevel ? <span className="tag">run-level</span> : null}
              </div>
              {f.detail ? (
                <p className="finding-detail clamp-3" title={f.detail}>
                  {f.detail}
                </p>
              ) : null}
              <div className="finding-foot">
                <span className="muted small" title={RULES[f.rule]}>
                  rule {f.rule}
                </span>
                <StepChips steps={f.steps} label={`Steps for ${f.title}`} />
              </div>
            </li>
          ))}
        </ul>
      )}
      <p className="legend-note">
        skill-ci types the findings by rule: friction is a failed or low metric, a failed check, an exception or a dropped connection;
        opportunity is a skill that was not used, or a run-level suggestion (those cite no steps).
      </p>
    </section>
  );
}

export function CriteriaPanel({ criteria }: { criteria: Criteria }) {
  const all = criteria.total > 0 && criteria.passed === criteria.total;
  return (
    <section className="panel" id="criteria" aria-labelledby="criteria-h">
      <header className="panel-head">
        <h2 id="criteria-h">Eval criteria</h2>
        <span className={all ? "tone-text-ok strong" : "strong"}>
          {criteria.passed} of {criteria.total} passed
        </span>
      </header>
      {criteria.behaviors.length === 0 ? <Empty>No expected behaviors for this case.</Empty> : null}
      {criteria.behaviors.length > 0 ? (
        <>
          <div className="criteria-group-head">
            <h3>Expected behaviors</h3>
            <StepChips steps={criteria.behaviorSteps} label="Steps the behavior check cites" />
          </div>
          <ol className="criteria-list">
            {criteria.behaviors.map((b) => (
              <li key={b.index} className="criterion">
                <PassBadge passed={b.passed} />
                <div>
                  <div className="criterion-title">
                    <span className="muted">{b.index}.</span> {b.text}
                  </div>
                  {b.reason ? (
                    <div className="criterion-reason clamp-2" title={b.reason}>
                      {b.reason}
                    </div>
                  ) : null}
                </div>
              </li>
            ))}
          </ol>
          {criteria.behaviorSteps.length ? <p className="legend-note">Step links come from the behavior check as a whole, not from each behavior.</p> : null}
        </>
      ) : null}
      {criteria.accuracy ? (
        <>
          <div className="criteria-group-head">
            <h3>Accuracy</h3>
            <StepChips steps={criteria.accuracy.steps} label="Steps the accuracy judge cites" />
          </div>
          <ul className="named-checks">
            {criteria.accuracy.criteria.map((c) => (
              <li key={c.name} className={c.passed ? "tone-ok" : "tone-bad"}>
                <span aria-hidden="true">{c.passed ? "✓" : "✕"}</span> <span className="mono">{c.name}</span>
                <span className="sr-only">{c.passed ? " passed" : " failed"}</span>
              </li>
            ))}
          </ul>
          <p className="criterion-reason clamp-3" title={criteria.accuracy.reason}>
            {criteria.accuracy.reason}
          </p>
        </>
      ) : null}
      {criteria.checks.length ? (
        <>
          <div className="criteria-group-head">
            <h3>Skill checks</h3>
          </div>
          <ul className="criteria-list">
            {criteria.checks.map((c) => (
              <li key={`${c.metric}-${c.name}`} className="criterion">
                <PassBadge passed={c.passed} />
                <div>
                  <div className="criterion-title">
                    {c.label} {c.score !== undefined ? <span className="num muted">{score(c.score)}</span> : null}
                  </div>
                  {c.reason ? (
                    <div className="criterion-reason clamp-2" title={c.reason}>
                      {c.reason}
                    </div>
                  ) : null}
                  <StepChips steps={c.steps} label={`Steps for ${c.label}`} />
                </div>
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </section>
  );
}
