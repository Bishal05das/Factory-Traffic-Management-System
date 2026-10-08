import { Signal, title } from "@/lib/api";

export function Badge({ value }: { value: string }) {
  return (
    <span className={`badge badge-${value.toLowerCase()}`}>{title(value)}</span>
  );
}
export function Lamp({
  signal = "UNKNOWN",
  small = false,
}: {
  signal?: Signal;
  small?: boolean;
}) {
  return (
    <span
      aria-label={signal}
      className={`lamp lamp-${signal.toLowerCase()} ${small ? "lamp-small" : ""}`}
    />
  );
}
export function Notice({
  message,
  retry,
}: {
  message: string;
  retry?: () => void;
}) {
  return (
    <div className="notice" role="alert">
      <span>{message}</span>
      {retry && (
        <button className="button small secondary" onClick={retry}>
          Retry connection
        </button>
      )}
    </div>
  );
}
export function SectionHeading({
  eyebrow,
  title: text,
  children,
}: {
  eyebrow?: string;
  title: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="section-heading">
      <div>
        {eyebrow && <div className="eyebrow">{eyebrow}</div>}
        <h2>{text}</h2>
      </div>
      {children}
    </div>
  );
}
