// One session log entry, and the step chips that link to entries. Agent output
// is untrusted: every string here is rendered as text, never as HTML.
import { useId, useState, type ReactNode } from "react";
import { Link, useLocation } from "react-router";

import type { LogEntry, ToolCall } from "../api/types.ts";
import { Mark } from "../components/Status.tsx";
import { bytes, clock, offset } from "../format.ts";

const FOLD_LINES = 12;
const FOLD_CHARS = 1200;

/** FoldText is preformatted untrusted text, folded with "Show more" when it is long. */
export function FoldText({ text, className = "" }: { text: string; className?: string }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const long = text.split("\n").length > FOLD_LINES || text.length > FOLD_CHARS;
  return (
    <>
      <pre id={id} className={`entry-text ${className}${long && !open ? " folded-long" : ""}`}>
        {text}
      </pre>
      {long ? (
        <button type="button" className="link-button" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
          {open ? "Show less" : "Show more"}
        </button>
      ) : null}
    </>
  );
}

/** stepTarget is the log anchor of an ATIF step: its first entry, s-<step>. */
export function stepTarget(step: number): string {
  return `s-${step}`;
}

/** useLogPath is the Log tab of the current trial, so a step link works from every tab. */
export function useLogPath(): { pathname: string; search: string } {
  const { pathname, search } = useLocation();
  const m = /^(.*\/trials\/[^/]+)(?:\/(?:files|judge|network))?\/?$/.exec(pathname);
  return { pathname: m?.[1] ?? pathname, search };
}

/** EntryLink links one log entry by its anchor id, on the Log tab. */
export function EntryLink({ id, children, className, label }: { id: string; children: ReactNode; className?: string; label?: string }) {
  const to = useLogPath();
  return (
    <Link to={{ ...to, hash: id }} className={className} aria-label={label}>
      {children}
    </Link>
  );
}

/** StepChips are the step numbers a criterion or finding cites; each jumps to the log entry. */
export function StepChips({ steps, label = "Steps" }: { steps: number[]; label?: string }) {
  if (steps.length === 0) return null;
  return (
    <ul className="step-chips" aria-label={label}>
      {steps.map((s) => (
        <li key={s}>
          <EntryLink id={stepTarget(s)} className="step-chip" label={`Step ${s}`}>
            {s}
          </EntryLink>
        </li>
      ))}
    </ul>
  );
}

function ToolView({ tool, error }: { tool: ToolCall; error: boolean }) {
  const out = tool.output;
  const failed = out ? out.error : error;
  return (
    <div className="tool">
      <details className="tool-input">
        <summary>input</summary>
        <pre className="entry-text">{tool.input}</pre>
      </details>
      {out ? (
        <details className="tool-output" open={failed}>
          <summary>
            output <span className="muted">· {bytes(out.bytes)} · {out.lines} lines</span>
            {failed ? <span className="tone-text-bad"> · error</span> : null}
            {out.truncated ? <span className="muted"> · preview</span> : null}
            {out.shortened ? <span className="muted"> · shortened in the bundle</span> : null}
          </summary>
          <pre className="entry-text output">{out.preview}</pre>
          <p className="output-foot">
            {out.truncated ? "The preview stops at 4 KiB or 40 lines. " : null}
            {out.shortened ? "The bundle kept the head and tail of this output. " : null}
            {/* The full text is a server response (text/plain), not an app route: full page load. */}
            <a href={out.href}>Full output as text</a>
          </p>
        </details>
      ) : (
        <p className="muted small">{failed ? "No output; the call failed." : "No output recorded."}</p>
      )}
    </div>
  );
}

/** EntryView renders one log entry as a list item that j and k can focus. */
export function EntryView({ e, target, compact = false }: { e: LogEntry; target: boolean; compact?: boolean }) {
  const isTool = !!e.tool;
  const failed = e.tool?.output ? e.tool.output.error : e.error;
  const classes = ["entry", `cat-${e.category}`, e.error ? "is-error" : "", target ? "is-target" : "", e.category === "think" ? "is-dim" : ""]
    .filter(Boolean)
    .join(" ");
  return (
    <li id={compact ? undefined : e.id} className={classes} tabIndex={compact ? undefined : -1} aria-label={e.label} data-entry={e.id}>
      <div className="entry-gutter" aria-hidden="true">
        <span className="entry-step">{e.step > 0 ? e.step : e.source === "judge" ? "J" : e.source === "network" ? "N" : ""}</span>
        <span className="entry-time" title={e.at ? clock(e.at) : undefined}>
          {offset(e.offsetSec)}
        </span>
      </div>
      <div className="entry-body">
        <div className="entry-head">
          <span className="cat-tag">{e.category}</span>
          <span className={isTool ? "entry-title mono" : "entry-title"}>{e.title}</span>
          {isTool ? <Mark ok={!failed} /> : null}
          {e.flow ? <Mark ok={e.flow.verdict === "FORWARDED"} okText="forwarded" badText={e.flow.verdict.toLowerCase()} /> : null}
          {!isTool && !e.flow && e.error ? <Mark ok={false} /> : null}
        </div>
        {e.category === "think" && e.text ? (
          <details className="think">
            <summary>reasoning</summary>
            <FoldText text={e.text} />
          </details>
        ) : e.text ? (
          <FoldText text={e.text} />
        ) : null}
        {e.textTruncated ? <p className="muted small">The text was cut for the page.</p> : null}
        {e.tool && !compact ? <ToolView tool={e.tool} error={e.error} /> : null}
        {e.flow ? (
          <p className="flow-line mono">
            {e.flow.ip} {e.flow.protocol}/{e.flow.port} · {e.flow.count}x · {clock(e.flow.firstAt)} to {clock(e.flow.lastAt)}
            {e.flow.reason ? ` · ${e.flow.reason}` : ""}
          </p>
        ) : null}
      </div>
    </li>
  );
}
