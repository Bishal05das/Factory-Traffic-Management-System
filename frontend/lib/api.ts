export const directions = ["NORTH", "SOUTH", "EAST", "WEST"] as const;
export type Direction = (typeof directions)[number];
export type Signal = "RED" | "YELLOW" | "GREEN" | "UNKNOWN";
export type VehicleType =
  | "TRUCK"
  | "FORKLIFT"
  | "EMPLOYEE_VEHICLE"
  | "EMERGENCY";
export type Vehicle = {
  vehicle_id: string;
  direction: Direction;
  vehicle_type: VehicleType;
  arrival_event_id: string;
  accepted_at: string;
  emergency_active: boolean;
  emergency_expires_at?: string;
};
export type Command = {
  command_id: string;
  direction: Direction;
  requested_state: Signal;
  status: string;
  generation: number;
  expires_at: string;
};
export type Device = {
  device_type: string;
  direction?: Direction;
  status: string;
};
export type Config = {
  id: string;
  name: string;
  phases: { id: string; directions: Direction[] }[];
  policy: Record<string, unknown>;
};
export type Status = {
  junction_id: string;
  name: string;
  revision: number;
  generation: number;
  mode: string;
  stage: string;
  phase: string;
  target_phase: string;
  controller_status: string;
  desired_signals: Partial<Record<Direction, Signal>>;
  actual_signals: Partial<Record<Direction, Signal>>;
  queues: Partial<Record<Direction, number>>;
  vehicles: Vehicle[];
  emergencies: Vehicle[];
  devices: Device[];
  alerts: { code: string; message: string }[];
  manual: { direction: Direction; expires_at: string } | null;
  pending_batch: {
    batch_id: string;
    commands: Command[];
    deadline: string;
  } | null;
  transition_deadline: string | null;
  sequence_high_water: Partial<Record<Direction, number>>;
  config: Config;
};
export type Activity = {
  id: number;
  event_type: string;
  timestamp: string;
  direction?: Direction;
  details: Record<string, unknown>;
};
export type SensorEvent = {
  event_id: string;
  junction_id: string;
  direction: Direction;
  event_type: "VEHICLE_ARRIVED" | "VEHICLE_CLEARED";
  vehicle_id: string;
  vehicle_type?: VehicleType;
  sequence_no: number;
  timestamp: string;
};

export async function request<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const timeout = AbortSignal.timeout(12_000);
  const response = await fetch(`/api${path}`, {
    method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  });
  const value = await response.json().catch(() => {
    throw new Error("The backend returned an unreadable response.");
  });
  if (!response.ok)
    throw new Error(value.message || `Request failed (${response.status}).`);
  return value as T;
}

export function validateStatus(status: Status): Status {
  if (
    !status ||
    typeof status.junction_id !== "string" ||
    typeof status.mode !== "string" ||
    typeof status.stage !== "string" ||
    typeof status.phase !== "string" ||
    typeof status.controller_status !== "string" ||
    !status.actual_signals ||
    typeof status.actual_signals !== "object" ||
    !status.desired_signals ||
    typeof status.desired_signals !== "object" ||
    !status.queues ||
    typeof status.queues !== "object" ||
    !Array.isArray(status.vehicles) ||
    !Array.isArray(status.emergencies) ||
    !Array.isArray(status.devices) ||
    !Array.isArray(status.alerts)
  )
    throw new Error("The backend returned incomplete junction data.");
  if (status.pending_batch && !Array.isArray(status.pending_batch.commands))
    throw new Error("The backend returned incomplete controller command data.");
  const colors = ["RED", "YELLOW", "GREEN", "UNKNOWN"];
  for (const direction of directions) {
    for (const signal of [
      status.actual_signals[direction],
      status.desired_signals[direction],
    ]) {
      if (signal !== undefined && !colors.includes(signal))
        throw new Error("The backend returned an invalid signal state.");
    }
    const count = status.queues[direction];
    if (count !== undefined && (!Number.isInteger(count) || count < 0))
      throw new Error("The backend returned an invalid queue count.");
  }
  if (
    [...status.vehicles, ...status.emergencies].some(
      (v) =>
        !v ||
        typeof v.vehicle_id !== "string" ||
        !directions.includes(v.direction) ||
        typeof v.vehicle_type !== "string" ||
        typeof v.accepted_at !== "string",
    ) ||
    status.devices.some(
      (d) =>
        !d || typeof d.device_type !== "string" || typeof d.status !== "string",
    ) ||
    status.alerts.some(
      (a) => !a || typeof a.message !== "string" || typeof a.code !== "string",
    )
  )
    throw new Error("The backend returned incomplete vehicle or device data.");
  if (
    status.pending_batch?.commands.some(
      (c) =>
        !c ||
        typeof c.command_id !== "string" ||
        !directions.includes(c.direction) ||
        !colors.includes(c.requested_state) ||
        typeof c.status !== "string",
    )
  )
    throw new Error("The backend returned incomplete controller command data.");
  if (
    status.manual &&
    (!directions.includes(status.manual.direction) ||
      typeof status.manual.expires_at !== "string")
  )
    throw new Error("The backend returned incomplete manual control data.");
  return {
    ...status,
    vehicles: status.vehicles || [],
    emergencies: status.emergencies || [],
    devices: status.devices || [],
    alerts: status.alerts || [],
    sequence_high_water: status.sequence_high_water || {},
  };
}

export function phaseLabel(status: Status): string {
  if (
    status.mode === "FAILURE" ||
    directions.some(
      (d) =>
        !status.actual_signals[d] || status.actual_signals[d] === "UNKNOWN",
    )
  )
    return "Unverified";
  if (directions.every((d) => status.actual_signals[d] === "RED"))
    return "All red";
  return status.phase ? status.phase.replaceAll("_", " + ") : "Transitioning";
}

export const title = (value: string) =>
  value
    .toLowerCase()
    .replaceAll("_", " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
export const clock = (value: string) =>
  new Date(value).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
