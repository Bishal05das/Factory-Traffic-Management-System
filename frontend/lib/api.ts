export const directions = ["NORTH", "SOUTH", "EAST", "WEST"] as const;
export type Direction = typeof directions[number];
export type Signal = "RED" | "YELLOW" | "GREEN" | "UNKNOWN";
export type VehicleType = "TRUCK" | "FORKLIFT" | "EMPLOYEE_VEHICLE" | "EMERGENCY";
export type Vehicle = { vehicle_id: string; direction: Direction; vehicle_type: VehicleType; arrival_event_id: string; accepted_at: string; emergency_active: boolean; emergency_expires_at?: string };
export type Command = { command_id: string; direction: Direction; requested_state: Signal; status: string; generation: number; expires_at: string };
export type Device = { device_type: string; direction?: Direction; status: string };
export type Config = { id: string; name: string; phases: { id: string; directions: Direction[] }[]; policy: Record<string, unknown> };
export type Status = {
  junction_id: string; name: string; revision: number; generation: number; mode: string; stage: string; phase: string; target_phase: string; controller_status: string;
  desired_signals: Partial<Record<Direction, Signal>>; actual_signals: Partial<Record<Direction, Signal>>; queues: Partial<Record<Direction, number>>;
  vehicles: Vehicle[]; emergencies: Vehicle[]; devices: Device[]; alerts: { code: string; message: string }[];
  manual: { direction: Direction; expires_at: string } | null; pending_batch: { batch_id: string; commands: Command[]; deadline: string } | null;
  transition_deadline: string | null; sequence_high_water: Partial<Record<Direction, number>>; config: Config;
};
export type Activity = { id: number; event_type: string; timestamp: string; direction?: Direction; details: Record<string, unknown> };
export type SensorEvent = { event_id: string; junction_id: string; direction: Direction; event_type: "VEHICLE_ARRIVED" | "VEHICLE_CLEARED"; vehicle_id: string; vehicle_type?: VehicleType; sequence_no: number; timestamp: string };

export async function request<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const timeout = AbortSignal.timeout(12_000);
  const response = await fetch(`/api${path}`, { method: body === undefined ? "GET" : "POST", headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body), cache: "no-store", signal: signal ? AbortSignal.any([signal, timeout]) : timeout });
  const value = await response.json().catch(() => { throw new Error("The backend returned an unreadable response."); });
  if (!response.ok) throw new Error(value.message || `Request failed (${response.status}).`);
  return value as T;
}

export function validateStatus(status: Status): Status {
  if (!status || typeof status.junction_id !== "string" || !status.actual_signals || !status.desired_signals || !status.queues) throw new Error("The backend returned incomplete junction data.");
  if (status.pending_batch && !Array.isArray(status.pending_batch.commands)) throw new Error("The backend returned incomplete controller command data.");
  return { ...status, vehicles: status.vehicles || [], emergencies: status.emergencies || [], devices: status.devices || [], alerts: status.alerts || [], sequence_high_water: status.sequence_high_water || {} };
}

export const title = (value: string) => value.toLowerCase().replaceAll("_", " ").replace(/\b\w/g, (c) => c.toUpperCase());
export const clock = (value: string) => new Date(value).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
