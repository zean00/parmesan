type PillProps = {
  label: string;
  tone?: "neutral" | "positive" | "attention" | "danger";
};

export function Pill({ label, tone = "neutral" }: PillProps) {
  return <span className={`pill pill--${tone}`}>{label}</span>;
}
