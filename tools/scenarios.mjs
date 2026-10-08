// End-to-end verification against a running API, without external packages.
// Stop the automatic simulator first: this script supplies controlled ACKs.
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { execFileSync } from "node:child_process";

const base = (process.env.API_URL || "http://localhost:8080").replace(/\/$/, "");
const id = `CHECK-${randomUUID().slice(0, 8)}`;
const path = `/api/junctions/${id}`;
const directions = ["NORTH", "SOUTH", "EAST", "WEST"];
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
let lastACK;

async function api(route, body, expected) {
  const response = await fetch(`${base}${route}`, { method: body ? "POST" : "GET", headers: { "Content-Type": "application/json" }, body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(10_000) });
  const value = await response.json();
  if (expected) assert.equal(response.status, expected, JSON.stringify(value));
  else assert.ok(response.ok, `${response.status}: ${JSON.stringify(value)}`);
  return value;
}
const status = () => api(`${path}/status`);
const control = (command, direction) => api(`${path}/commands`, { command, direction });

function safe(s) {
  for (const signals of [s.desired_signals, s.actual_signals]) {
    const active = new Set(directions.filter((d) => ["GREEN", "YELLOW"].includes(signals[d])).map((d) => s.config.phases.find((p) => p.directions.includes(d)).id));
    assert.ok(active.size <= 1, "Conflicting permissive signals");
  }
  assert.ok(Object.values(s.queues).every((count) => count >= 0));
}

async function acknowledge() {
  const feed = await api(`${path}/controller-commands`);
  for (const command of feed.commands) {
    lastACK = { command_id: command.command_id, junction_id: id, status: "ACK", actual_state: command.requested_state, timestamp: new Date().toISOString() };
    await api("/api/controller-events", lastACK);
  }
}

async function until(predicate, label, timeout = 12_000, ack = true) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    let s;
    try { s = await status(); }
    catch (error) {
      // Read-only readiness probes may see a reset socket during a real
      // restart. Retry observations, never mutation requests or assertions.
      if (!(error instanceof TypeError)) throw error;
      await delay(150); continue;
    }
    safe(s);
    if (predicate(s)) return s;
    if (ack) await acknowledge();
    await delay(150);
  }
  throw new Error(`Timed out: ${label}; ${JSON.stringify(await status())}`);
}

async function online() {
  for (const kind of ["SIGNAL_CONTROLLER", "SIGNAL", "SENSOR"]) {
    for (const direction of kind === "SIGNAL_CONTROLLER" ? [undefined] : directions) {
      await api("/api/device-events", { event_id: randomUUID(), junction_id: id, device_type: kind, direction, status: "ONLINE", timestamp: new Date().toISOString() });
    }
  }
}

async function event(vehicle_id, direction, vehicle_type = "TRUCK", event_type = "VEHICLE_ARRIVED") {
  const s = await status();
  return { event_id: randomUUID(), junction_id: id, vehicle_id, direction, vehicle_type: event_type === "VEHICLE_ARRIVED" ? vehicle_type : undefined, event_type, sequence_no: s.sequence_high_water[direction] + 1, timestamp: new Date().toISOString() };
}

const template = await api("/api/junctions/A");
await api("/api/junctions", { ...template, id, name: `Verification ${id}`, policy: { ...template.policy, green_seconds: 2, yellow_seconds: 1, clearance_seconds: 1, ack_seconds: 5, manual_seconds: 30, emergency_seconds: 30, starvation_seconds: 10 } }, 201);
console.log(`Isolated verification junction ${id}; short test timing, Junction A configuration unchanged.`);
await online(); await until((s) => s.stage === "ALL_RED_HOLD", "initial red confirmation");

const truck = await event("north-truck", "NORTH", "TRUCK");
await api("/api/sensor-events", truck, 201);
const employee = await event("east-employee", "EAST", "EMPLOYEE_VEHICLE");
await api("/api/sensor-events", employee, 201);
await until((s) => s.phase === "NORTH_SOUTH" && s.stage === "GREEN", "normal priority north green");
console.log("PASS normal traffic and vehicle priority");

assert.equal((await api("/api/sensor-events", truck, 200)).outcome, "duplicate");
assert.equal((await status()).queues.NORTH, 1);
await api("/api/sensor-events", { ...truck, vehicle_id: "conflicting-reuse" }, 409);
assert.equal((await api("/api/controller-events", lastACK)).outcome, "duplicate");
console.log("PASS duplicate event, conflicting ID reuse, and duplicate ACK");

const emergency = await event("emergency", "EAST", "EMERGENCY");
await api("/api/sensor-events", emergency);
let s = await status(); assert.equal(s.stage, "WAIT_YELLOW"); assert.equal(s.desired_signals.EAST, "RED");
await until((s) => s.phase === "EAST_WEST" && s.stage === "GREEN", "safe emergency preemption");
await control("MANUAL_GREEN_REQUEST", "NORTH"); assert.equal((await status()).mode, "EMERGENCY");
await api("/api/sensor-events", await event("emergency", "EAST", undefined, "VEHICLE_CLEARED"));
await until((s) => s.mode === "MANUAL" && s.phase === "NORTH_SOUTH" && s.stage === "GREEN", "manual intent restored");
await control("RETURN_TO_AUTOMATIC"); assert.equal((await status()).mode, "AUTOMATIC");
console.log("PASS emergency preemption, clearance, manual override and return to automatic");

const ghost = await event("ghost", "WEST", undefined, "VEHICLE_CLEARED"); ghost.sequence_no = 100;
await api("/api/sensor-events", ghost);
await api("/api/sensor-events", { ...ghost, event_id: randomUUID(), event_type: "VEHICLE_ARRIVED", vehicle_type: "FORKLIFT", sequence_no: 99 }, 409);
assert.equal((await status()).queues.WEST, 0);
console.log("PASS unmatched clearance tombstone and delayed arrival rejection");

await api("/api/device-events", { event_id: randomUUID(), junction_id: id, device_type: "SIGNAL_CONTROLLER", status: "OFFLINE", timestamp: new Date().toISOString() });
s = await status(); assert.equal(s.mode, "FAILURE"); assert.ok(Object.values(s.actual_signals).every((signal) => signal === "UNKNOWN"));
await online(); assert.equal((await status()).stage, "FAILURE_STOP");
await control("RECOVER"); await until((s) => s.stage === "GREEN", "explicit offline recovery");
const opposite = (await status()).phase === "NORTH_SOUTH" ? "EAST" : "NORTH";
await control("MANUAL_GREEN_REQUEST", opposite);
await until((s) => s.stage === "WAIT_GREEN", "pending green for timeout");
await until((s) => s.stage === "FAILURE_STOP", "unacknowledged command timeout", 8000, false);
s = await status(); assert.equal(s.mode, "FAILURE"); assert.ok(Object.values(s.desired_signals).every((signal) => signal === "RED"));
await control("RECOVER"); await until((s) => s.stage === "GREEN", "timeout recovery");
console.log("PASS offline controller, ACK timeout, unknown evidence, and explicit recovery");

const before = (await status()).vehicles.length;
const concurrent = await Promise.all(Array.from({ length: 16 }, (_, i) => event(`concurrent-${i}`, directions[i % 4], i === 1 ? "EMERGENCY" : "TRUCK")));
const operations = concurrent.map((payload) => api("/api/sensor-events", payload));
operations.push(api("/api/sensor-events", concurrent[1]));
operations.push(control("MANUAL_GREEN_REQUEST", "WEST"));
operations.push(api("/api/controller-events", lastACK));
await Promise.all(operations);
s = await status(); safe(s); assert.equal(s.vehicles.length, before + 16);
console.log("PASS simultaneous arrivals, emergency, manual intent, duplicate event, and ACK");

if (process.argv.includes("--restart")) {
  const persisted = await status();
  execFileSync("docker", ["compose", "restart", "backend"], { stdio: "inherit" });
  const recovered = await until((s) => s.stage === "RECOVERING", "backend restart evidence invalidation", 20_000, false);
  assert.equal(recovered.vehicles.length, persisted.vehicles.length);
  assert.ok(Object.values(recovered.actual_signals).every((signal) => signal === "UNKNOWN"));
  assert.equal(recovered.manual?.direction, persisted.manual?.direction);
  assert.equal((await api("/api/sensor-events", truck, 200)).outcome, "duplicate");
  await online(); await until((s) => s.stage === "ALL_RED_HOLD", "fresh restart red confirmation");
  const history = await api(`${path}/history?limit=200`);
  assert.ok(history.some((item) => item.event_type === "BACKEND_RESTART_RECOVERY"));
  console.log("PASS persisted queue, intent, deduplication and history across actual backend restart");
}

// Leave no test traffic active. Preserve the junction and audit as evidence.
for (const vehicle of (await status()).vehicles) await api("/api/sensor-events", await event(vehicle.vehicle_id, vehicle.direction, undefined, "VEHICLE_CLEARED"));
if ((await status()).mode !== "FAILURE") await control("RETURN_TO_AUTOMATIC");
console.log(`Verification complete. History is available for ${id}.`);
