import { randomUUID } from "node:crypto";
import { readFile, mkdir, writeFile, rename } from "node:fs/promises";
import { dirname } from "node:path";
import { apply, fence, initialController } from "./controller-model.mjs";

const api = (process.env.API_URL || "http://localhost:8080").replace(/\/$/, "");
const stateFile = process.env.SIMULATOR_STATE_FILE || ".simulator-state.json";
let controllers = {};
try { controllers = JSON.parse(await readFile(stateFile, "utf8")); }
catch (error) { if (error.code !== "ENOENT") throw error; }

async function persist() {
  await mkdir(dirname(stateFile), { recursive: true });
  await writeFile(`${stateFile}.tmp`, JSON.stringify(controllers), { mode: 0o600 });
  await rename(`${stateFile}.tmp`, stateFile);
}

async function request(path, body) {
  const response = await fetch(`${api}${path}`, { method: body ? "POST" : "GET", headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(3000) });
  const value = await response.json();
  if (!response.ok) throw new Error(`${response.status}: ${value.message || response.statusText}`);
  return value;
}

async function cycle() {
  const junctions = await request("/api/junctions");
  for (let status of junctions) {
    const id = status.junction_id;
    const controller = controllers[id] ||= initialController();
    // Simulated devices really exist in this process. Announce UNKNOWN health
    // after startup/restart, but never undo an evaluator's OFFLINE report.
    for (const device of status.devices) {
      if (device.status !== "UNKNOWN") continue;
      await request("/api/device-events", { event_id: randomUUID(), junction_id: id, device_type: device.device_type, direction: device.direction || undefined, status: "ONLINE", timestamp: new Date().toISOString() });
    }
    status = await request(`/api/junctions/${encodeURIComponent(id)}/status`);
    const feed = await request(`/api/junctions/${encodeURIComponent(id)}/controller-commands`);
    fence(controller, feed.generation);
    await persist();
    if (status.controller_status !== "ONLINE") continue;
    // REDs execute first. The domain still requires complete previous all-red
    // confirmation; this ordering adds the controller's physical interlock.
    const rank = { RED: 0, YELLOW: 1, GREEN: 2 };
    feed.commands.sort((a, b) => rank[a.requested_state] - rank[b.requested_state]);
    for (const command of feed.commands) {
      const signal = status.devices.find((d) => d.device_type === "SIGNAL" && d.direction === command.direction);
      if (signal?.status !== "ONLINE") continue;
      let actual;
      try { actual = apply(controller, command, status.config.phases); }
      catch (error) {
        console.error(`[${id}] refused ${command.command_id}: ${error.message}`);
        if (error.message.includes("interlock")) await request("/api/controller-events", { command_id: command.command_id, junction_id: id, status: "NACK", actual_state: controller.signals[command.direction], timestamp: new Date().toISOString() });
        continue;
      }
      await persist(); // Physical execution and its fence survive simulator restart.
      await request("/api/controller-events", { command_id: command.command_id, junction_id: id, status: "ACK", actual_state: actual, timestamp: new Date().toISOString() });
    }
  }
}

let running = true;
process.on("SIGINT", () => { running = false; });
process.on("SIGTERM", () => { running = false; });
console.log(`REST controller simulator enabled for ${api}; fence storage: ${stateFile}`);
while (running) {
  try { await cycle(); } catch (error) { console.error(`Simulator: ${error.message}`); }
  if (running) await new Promise((resolve) => setTimeout(resolve, 250));
}
