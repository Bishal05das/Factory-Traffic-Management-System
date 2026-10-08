"use client";
import { useState } from "react";
import { Direction, directions, request, SensorEvent, Status, title, Vehicle, VehicleType } from "@/lib/api";
import { Badge, SectionHeading } from "./shared";

export type Action = (label: string, path: string, body: unknown) => Promise<boolean>;

export function TrafficSimulation({ status, action, busy }: { status: Status; action: Action; busy: boolean }) {
  const [direction, setDirection] = useState<Direction>("NORTH");
  const [kind, setKind] = useState<VehicleType>("FORKLIFT");
  const [eventType, setEventType] = useState<SensorEvent["event_type"]>("VEHICLE_ARRIVED");
  const [vehicle, setVehicle] = useState("VH-101");
  const [last, setLast] = useState<SensorEvent | null>(null);
  const [error, setError] = useState(""); const [preparing, setPreparing] = useState(false);
  async function submit(id: string, d: Direction, type: SensorEvent["event_type"], vehicleType?: VehicleType) {
    setError(""); setPreparing(true);
    try {
      const fresh = await request<Status>(`/junctions/${encodeURIComponent(status.junction_id)}/status`);
      const payload: SensorEvent = { event_id: crypto.randomUUID(), junction_id: status.junction_id, direction: d, event_type: type, vehicle_id: id, vehicle_type: type === "VEHICLE_ARRIVED" ? vehicleType : undefined, sequence_no: (fresh.sequence_high_water?.[d] || 0) + 1, timestamp: new Date().toISOString() };
      setLast(payload);
      await action(type === "VEHICLE_ARRIVED" ? "Vehicle arrival" : "Vehicle clearance", "/sensor-events", payload);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Unable to prepare event."); } finally { setPreparing(false); }
  }
  const locked = busy || preparing;
  return <section className="card simulation-card"><SectionHeading eyebrow="TRAFFIC SIMULATION" title="Send a vehicle event"><span className="subtle">Queues change only after accepted events</span></SectionHeading>
    <form className="vehicle-form" onSubmit={(event) => { event.preventDefault(); void submit(vehicle, direction, eventType, kind); }}><label>Event<select value={eventType} onChange={(e) => setEventType(e.target.value as SensorEvent["event_type"])}><option value="VEHICLE_ARRIVED">Vehicle arrival</option><option value="VEHICLE_CLEARED">Vehicle clearance</option></select></label><label>Direction<select value={direction} onChange={(e) => setDirection(e.target.value as Direction)}>{directions.map((d) => <option key={d}>{d}</option>)}</select></label><label>Vehicle type<select value={kind} onChange={(e) => setKind(e.target.value as VehicleType)} disabled={eventType === "VEHICLE_CLEARED"}>{["FORKLIFT", "TRUCK", "EMPLOYEE_VEHICLE", "EMERGENCY"].map((type) => <option value={type} key={type}>{title(type)}</option>)}</select></label><label>Vehicle ID<input value={vehicle} onChange={(e) => setVehicle(e.target.value)} required maxLength={128} pattern="[A-Za-z0-9._-]+" /></label><button className="button primary" disabled={locked}>{preparing ? "Preparing…" : "Send event"}</button></form>
    <div className="simulation-actions"><button className="button danger-outline" disabled={locked} onClick={() => void submit(`EM-${crypto.randomUUID().slice(0, 8)}`, direction, "VEHICLE_ARRIVED", "EMERGENCY")}>Simulate emergency · {title(direction)}</button><button className="button secondary" disabled={locked || !last} onClick={() => last && void action("Duplicate replay", "/sensor-events", last)}>Replay last event</button>{last && <details><summary>Last event payload</summary><pre>{JSON.stringify(last, null, 2)}</pre></details>}</div>
    {error && <p className="inline-error" role="alert">{error}</p>}
    <div className="queue-heading"><h3>Waiting vehicles</h3><span>{status.vehicles.length} in queue</span></div>
    {!status.vehicles.length ? <div className="empty compact">No waiting vehicles. Add an arrival to begin.</div> : <div className="vehicle-list">{status.vehicles.map((v: Vehicle) => <div className="vehicle-row" key={v.vehicle_id}><span className="vehicle-direction">{v.direction.slice(0, 1)}</span><div><strong>{v.vehicle_id}</strong><small>{title(v.vehicle_type)} · {title(v.direction)} · {Math.max(0, Math.floor((Date.now() - Date.parse(v.accepted_at)) / 1000))}s waiting</small></div>{v.emergency_active && <Badge value="EMERGENCY" />}<button className="button small secondary" disabled={locked} onClick={() => void submit(v.vehicle_id, v.direction, "VEHICLE_CLEARED")}>Clear vehicle</button></div>)}</div>}
  </section>;
}
