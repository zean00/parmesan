type KeyValueListProps = {
  entries: Array<[string, unknown]>;
};

export function KeyValueList({ entries }: KeyValueListProps) {
  return (
    <dl className="kv-list">
      {entries.map(([key, value]) => (
        <div className="kv-list__row" key={key}>
          <dt>{key}</dt>
          <dd>{String(value ?? "n/a")}</dd>
        </div>
      ))}
    </dl>
  );
}
