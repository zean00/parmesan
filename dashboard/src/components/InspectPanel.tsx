import type { ReactNode } from "react";
import { JsonBlock } from "./JsonBlock";

type InspectPanelProps = {
  title: string;
  summary?: string;
  value: unknown;
  defaultOpen?: boolean;
  aside?: ReactNode;
};

export function InspectPanel({ title, summary, value, defaultOpen = false, aside }: InspectPanelProps) {
  return (
    <details className="inspect-panel" open={defaultOpen}>
      <summary className="inspect-panel__summary">
        <div>
          <strong>{title}</strong>
          {summary ? <span>{summary}</span> : null}
        </div>
        {aside ? <div className="inspect-panel__aside">{aside}</div> : null}
      </summary>
      <div className="inspect-panel__body">
        <JsonBlock value={value} />
      </div>
    </details>
  );
}
