// Keyboard shortcuts (CONTRACT.md, "Reporting site"): j and k move between log
// entries, / focuses the filter, g g and G jump to the first and last entry.
// Shortcuts never fire while the viewer types in a field or holds a modifier.
import { useEffect, useRef } from "react";

/** typing is true when a key event belongs to a text field, a select or an editable element. */
export function typing(e: KeyboardEvent): boolean {
  const t = e.target;
  if (!(t instanceof HTMLElement)) return false;
  if (t.isContentEditable) return true;
  const tag = t.tagName;
  if (tag === "TEXTAREA" || tag === "SELECT") return true;
  if (tag === "INPUT") {
    const type = (t as HTMLInputElement).type;
    return type !== "checkbox" && type !== "radio" && type !== "button" && type !== "submit";
  }
  return false;
}

function plainKey(e: KeyboardEvent): boolean {
  return !e.ctrlKey && !e.metaKey && !e.altKey && !typing(e);
}

/** useSlashFocus focuses an element when the viewer presses "/". */
export function useSlashFocus(target: () => HTMLElement | null | undefined): void {
  const ref = useRef(target);
  ref.current = target;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "/" || !plainKey(e)) return;
      const el = ref.current();
      if (!el) return;
      e.preventDefault();
      el.focus();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
}

export interface LogKeys {
  /** The visible entries, in order. */
  entries: () => HTMLElement[];
  /** The element "/" focuses. */
  filter: () => HTMLElement | null | undefined;
}

/** nextIndex is the entry j (+1) or k (-1) moves to from the current one (-1 when none has focus). */
export function nextIndex(current: number, step: 1 | -1, length: number): number {
  if (length === 0) return -1;
  if (current < 0) return step === 1 ? 0 : length - 1;
  return Math.max(0, Math.min(length - 1, current + step));
}

/** useLogKeys wires j, k, g g, G and / for a session log. */
export function useLogKeys(keys: LogKeys): void {
  const ref = useRef(keys);
  ref.current = keys;
  useEffect(() => {
    let lastG = 0;
    const focus = (el: HTMLElement | undefined) => {
      if (!el) return;
      el.focus();
      el.scrollIntoView?.({ block: "nearest" });
    };
    const onKey = (e: KeyboardEvent) => {
      if (!plainKey(e)) return;
      const { entries, filter } = ref.current;
      if (e.key === "/") {
        const el = filter();
        if (el) {
          e.preventDefault();
          el.focus();
        }
        return;
      }
      if (e.key !== "j" && e.key !== "k" && e.key !== "g" && e.key !== "G") return;
      const list = entries();
      if (list.length === 0) return;
      const active = document.activeElement;
      const current = list.findIndex((el) => el === active || (active instanceof Node && el.contains(active)));
      e.preventDefault();
      if (e.key === "G") {
        focus(list[list.length - 1]);
      } else if (e.key === "g") {
        const now = Date.now();
        if (now - lastG < 800) {
          focus(list[0]);
          lastG = 0;
        } else {
          lastG = now;
        }
      } else {
        focus(list[nextIndex(current, e.key === "j" ? 1 : -1, list.length)]);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
}
