import { compactJSON } from "../lib/format";

type JsonBlockProps = {
  value: unknown;
};

export function JsonBlock({ value }: JsonBlockProps) {
  return <pre className="json-block">{compactJSON(value)}</pre>;
}
