// The session log: category chips with counts, the Focus and All presets, jump
// links, and one entry per ATIF part. A link to #<entry id> (from a step chip,
// a jump link or another tab) shows that entry's category if it was hidden,
// then focuses the entry. Keyboard: j and k, g g and G, / for the chips.
import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation } from "react-router";

import { CATEGORIES, type Category, type LogEntry, type TrialLog } from "../api/types.ts";
import { Empty } from "../components/Page.tsx";
import { useLogKeys } from "../keyboard.ts";
import { EntryLink, EntryView } from "./Entry.tsx";

/** CATEGORY_TEXT says what each chip shows (its tooltip). */
export const CATEGORY_TEXT: Record<Category, string> = {
  msg: "Agent messages",
  user: "User turns, the prompt first",
  think: "Agent reasoning",
  tools: "Tool calls and their output",
  web: "Web access: fetch, search, curl, wget",
  pkg: "Package installs: pip, npm, apt and others",
  skill: "Skill use: the Skill tool, SKILL.md reads, loaded instructions",
  net: "Network flows from Hubble, placed at the step that ran",
  judge: "Judge results, one per metric",
  usage: "Token usage and cost per step",
  meta: "Metadata: stop reason, working directory, system steps",
};

/** resolveEntry finds the entry an anchor names: the id itself, or for s-<n> the first entry of step n. */
export function resolveEntry(entries: LogEntry[], id: string): LogEntry | undefined {
  const exact = entries.find((e) => e.id === id);
  if (exact) return exact;
  const m = /^s-(\d+)$/.exec(id);
  if (!m) return undefined;
  const step = Number(m[1]);
  return entries.find((e) => e.step === step);
}

function same(a: Set<Category>, b: Set<Category>): boolean {
  return a.size === b.size && [...a].every((c) => b.has(c));
}

const JUMPS: [keyof TrialLog["jumps"], string][] = [
  ["firstTool", "First tool call"],
  ["skill", "Skill activation"],
  ["firstError", "First error"],
  ["final", "Final answer"],
];

export function LogView({ log, empty }: { log: TrialLog; empty: string }) {
  const focusSet = useMemo(() => new Set(log.counts.filter((c) => c.focus).map((c) => c.category)), [log]);
  const allSet = useMemo(() => new Set<Category>(CATEGORIES), []);
  const [shown, setShown] = useState<Set<Category>>(focusSet);
  const [target, setTarget] = useState<string | null>(null);
  const pending = useRef<string | null>(null);
  const listRef = useRef<HTMLOListElement>(null);
  const chipRef = useRef<HTMLInputElement>(null);
  const location = useLocation();

  useEffect(() => {
    const id = decodeURIComponent(location.hash.replace(/^#/, ""));
    if (!id) return;
    const entry = resolveEntry(log.entries, id);
    if (!entry) {
      // Another anchor on the page, such as the criteria panel.
      document.getElementById(id)?.scrollIntoView?.({ block: "start" });
      return;
    }
    setShown((prev) => (prev.has(entry.category) ? prev : new Set([...prev, entry.category])));
    setTarget(entry.id);
    pending.current = entry.id;
  }, [location.hash, location.key, log]);

  useEffect(() => {
    const id = pending.current;
    if (!id) return;
    const el = document.getElementById(id);
    if (!el) return;
    pending.current = null;
    el.focus();
    el.scrollIntoView?.({ block: "center" });
  });

  useLogKeys({
    entries: () => Array.from(listRef.current?.children ?? []).filter((el): el is HTMLElement => el instanceof HTMLElement),
    filter: () => chipRef.current,
  });

  const visible = log.entries.filter((e) => shown.has(e.category));
  const preset = same(shown, focusSet) ? "focus" : same(shown, allSet) ? "all" : "custom";
  const toggle = (c: Category) => {
    const next = new Set(shown);
    if (next.has(c)) next.delete(c);
    else next.add(c);
    setShown(next);
  };
  const jumps = JUMPS.filter(([k]) => log.jumps[k]);

  if (log.entries.length === 0) return <Empty>{empty}</Empty>;

  return (
    <div className="log-wrap">
      <div className="log-toolbar">
        <fieldset className="chips">
          <legend className="chips-legend">
            Show <kbd title="Press / to focus the categories">/</kbd>
          </legend>
          {log.counts.map((c, i) => (
            <label key={c.category} className={`chip${shown.has(c.category) ? " chip-on" : ""}${c.count === 0 ? " chip-empty" : ""}`} title={CATEGORY_TEXT[c.category]}>
              <input
                ref={i === 0 ? chipRef : undefined}
                type="checkbox"
                className="chip-input"
                checked={shown.has(c.category)}
                onChange={() => toggle(c.category)}
                aria-label={`${c.category}, ${c.count} ${c.count === 1 ? "entry" : "entries"}`}
              />
              <span className="chip-name" aria-hidden="true">
                {c.category}
              </span>
              <span className="chip-count" aria-hidden="true">
                {c.count}
              </span>
            </label>
          ))}
        </fieldset>
        <div className="preset" role="group" aria-label="View">
          <button type="button" className="seg" aria-pressed={preset === "focus"} onClick={() => setShown(new Set(focusSet))}>
            Focus
          </button>
          <button type="button" className="seg" aria-pressed={preset === "all"} onClick={() => setShown(new Set(allSet))}>
            All
          </button>
        </div>
      </div>
      <div className="log-subbar">
        {jumps.length ? (
          <nav aria-label="Jump to" className="jumps">
            <span className="muted">Jump to</span>
            <ul className="inline-list">
              {jumps.map(([k, label]) => (
                <li key={k}>
                  <EntryLink id={log.jumps[k] ?? ""} className={k === "firstError" ? "tone-text-bad" : undefined}>
                    {label}
                  </EntryLink>
                </li>
              ))}
            </ul>
          </nav>
        ) : null}
        <span className="muted small" aria-live="polite">
          {visible.length} of {log.entries.length} entries
        </span>
        <span className="muted small keys-hint" aria-hidden="true">
          <kbd>j</kbd> <kbd>k</kbd> move · <kbd>g</kbd> <kbd>g</kbd> first · <kbd>G</kbd> last
        </span>
      </div>
      {visible.length === 0 ? (
        <Empty>No entries in the chosen categories.</Empty>
      ) : (
        <ol className="log" ref={listRef} aria-label="Session log">
          {visible.map((e) => (
            <EntryView key={e.id} e={e} target={target === e.id} />
          ))}
        </ol>
      )}
    </div>
  );
}
