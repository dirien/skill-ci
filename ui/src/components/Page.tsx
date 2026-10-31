// Shared page parts: the page heading, sections, notices, the metadata strip,
// the prompt box, and the loading and error states of an API call.
import { useEffect, useId, useState, type ReactNode } from "react";

import type { ApiError } from "../api/client.ts";
import type { ApiState } from "../api/useApi.ts";

/** Page is one page: its h1, an optional line under it, and the content. */
export function Page({
  title,
  docTitle,
  sub,
  aside,
  children,
}: {
  title: ReactNode;
  docTitle?: string;
  sub?: ReactNode;
  aside?: ReactNode;
  children?: ReactNode;
}) {
  const text = docTitle ?? (typeof title === "string" ? title : "");
  useEffect(() => {
    document.title = text ? `${text} - skill-ci` : "skill-ci";
  }, [text]);
  return (
    <section className="page" aria-labelledby="page-title">
      <header className="page-head">
        <div>
          <h1 id="page-title">{title}</h1>
          {sub ? <div className="page-sub">{sub}</div> : null}
        </div>
        {aside ? <div className="page-aside">{aside}</div> : null}
      </header>
      {children}
    </section>
  );
}

/** Section is a titled block with an h2 that labels it. */
export function Section({
  title,
  id,
  extra,
  children,
  className,
}: {
  title: string;
  id?: string;
  extra?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  const auto = useId();
  const hid = `${id ?? auto}-h`;
  return (
    <section className={`section${className ? ` ${className}` : ""}`} id={id} aria-labelledby={hid}>
      <header className="section-head">
        <h2 id={hid}>{title}</h2>
        {extra ? <div className="section-extra">{extra}</div> : null}
      </header>
      {children}
    </section>
  );
}

/** Notice is a one-paragraph message in a status tone. */
export function Notice({ tone = "neutral", icon, children, role }: { tone?: "ok" | "bad" | "warn" | "info" | "neutral"; icon?: string; children: ReactNode; role?: "alert" | "status" }) {
  return (
    <div className={`notice tone-${tone}`} role={role}>
      {icon ? (
        <span className="notice-icon" aria-hidden="true">
          {icon}
        </span>
      ) : null}
      <div>{children}</div>
    </div>
  );
}

export function ErrorView({ error }: { error: ApiError }) {
  const title =
    error.code === "withheld" ? "Withheld" : error.status === 404 ? "Not found" : error.status === 502 ? "Registry error" : "Cannot load this page";
  return (
    <Notice tone="bad" icon="✕" role="alert">
      <strong>{title}</strong> {error.message}
    </Notice>
  );
}

/** Loaded renders children with the data, or the loading or error state. */
export function Loaded<T>({ state, children }: { state: ApiState<T>; children: (data: T) => ReactNode }) {
  if (state.status === "loading")
    return (
      <p className="muted loading" aria-live="polite">
        Loading...
      </p>
    );
  if (state.status === "error") return <ErrorView error={state.error} />;
  return <>{children(state.data)}</>;
}

/** MetaItem is one label and value of a metadata strip; a missing value leaves the item out. */
export interface MetaItem {
  label: string;
  value: ReactNode;
  wide?: boolean;
}

/** MetaStrip is a row of small uppercase labels with their values (a <dl>). */
export function MetaStrip({ items, label }: { items: (MetaItem | null | false | undefined)[]; label?: string }) {
  const shown = items.filter((i): i is MetaItem => !!i && i.value !== undefined && i.value !== null && i.value !== "");
  return (
    <dl className="meta-strip" aria-label={label}>
      {shown.map((i) => (
        <div key={i.label} className={i.wide ? "meta-wide" : undefined}>
          <dt>{i.label}</dt>
          <dd>{i.value}</dd>
        </div>
      ))}
    </dl>
  );
}

const PROMPT_LINES = 4;
const PROMPT_CHARS = 360;

/** PromptBox shows untrusted text in a wrapping preformatted box, folded to a few lines with "Show more". */
export function PromptBox({ text, label = "Prompt" }: { text: string; label?: string }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const long = text.split("\n").length > PROMPT_LINES || text.length > PROMPT_CHARS;
  return (
    <div className="prompt">
      <div className="prompt-label">{label}</div>
      <pre id={id} className={long && !open ? "prompt-text folded" : "prompt-text"}>
        {text}
      </pre>
      {long ? (
        <button type="button" className="link-button" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
          {open ? "Show less" : "Show more"}
        </button>
      ) : null}
    </div>
  );
}

/** Empty is the muted line for an empty list. */
export function Empty({ children }: { children: ReactNode }) {
  return <p className="muted empty">{children}</p>;
}
