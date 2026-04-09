import type { ReactNode } from "react";

type PageHeaderProps = {
  eyebrow?: string;
  title: string;
  summary?: string;
  actions?: ReactNode;
};

export function PageHeader({ eyebrow, title, summary, actions }: PageHeaderProps) {
  return (
    <header className="workspace__header">
      <div>
        {eyebrow ? <p className="workspace__eyebrow">{eyebrow}</p> : null}
        <h2>{title}</h2>
        {summary ? <p className="brand-copy">{summary}</p> : null}
      </div>
      {actions ? <div className="workspace__status">{actions}</div> : null}
    </header>
  );
}
