type PillProps = {
  label: string;
  tone?: "neutral" | "positive" | "attention";
};

export function Pill({ label, tone = "neutral" }: PillProps) {
  return <span className={`pill pill--${tone}`}>{label}</span>;
}
