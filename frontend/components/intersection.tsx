import { directions, Status, title } from "@/lib/api";
import { Badge, Lamp, SectionHeading } from "./shared";

export function Intersection({ status }: { status: Status }) {
  return (
    <section className="card intersection-card">
      <SectionHeading eyebrow="LIVE INTERSECTION" title="Physical signal state">
        <Badge value={status.stage || "UNKNOWN"} />
      </SectionHeading>
      <div className="intersection" aria-label="Junction intersection">
        <div className="road road-horizontal" />
        <div className="road road-vertical" />
        <div className="junction-center">
          <span>{status.junction_id}</span>
          <small>
            {status.phase
              ? status.phase.replaceAll("_", " + ")
              : "ALL RED / UNVERIFIED"}
          </small>
        </div>
        {directions.map((direction) => (
          <div
            className={`signal-node node-${direction.toLowerCase()}`}
            key={direction}
          >
            <div className="node-heading">
              {direction}
              <span>{status.queues[direction] ?? "?"} waiting</span>
            </div>
            <div className="signal-box">
              <Lamp signal={status.actual_signals[direction]} />
              <strong>
                {title(status.actual_signals[direction] || "UNKNOWN")}
              </strong>
            </div>
            <div className="desired-state">
              <Lamp signal={status.desired_signals[direction]} small />{" "}
              Requested {title(status.desired_signals[direction] || "UNKNOWN")}
            </div>
          </div>
        ))}
      </div>
      <div className="intersection-legend">
        <span>
          <i className="legend-solid" /> Filled lamp = controller-confirmed
        </span>
        <span>Requests await physical confirmation</span>
      </div>
    </section>
  );
}
