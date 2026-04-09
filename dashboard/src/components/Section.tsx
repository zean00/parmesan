import type { ReactNode } from "react";

type SectionProps = {
  eyebrow?: string;
  title: string;
  summary?: string;
  actions?: ReactNode;
  children: ReactNode;
};

export function Section({ eyebrow, title, summary, actions, children }: SectionProps) {
  return (
    <section className="section">
      <header className="section__header">
        <div>
          {eyebrow ? <p className="section__eyebrow">{eyebrow}</p> : null}
          <h2>{title}</h2>
          {summary ? <p className="section__summary">{summary}</p> : null}
        </div>
        {actions ? <div className="section__actions">{actions}</div> : null}
      </header>
      <div className="section__body">{children}</div>
    </section>
  );
}
