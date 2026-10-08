export const directions = ["NORTH", "SOUTH", "EAST", "WEST"];

export function initialController() {
  return { generation: 0, signals: Object.fromEntries(directions.map((d) => [d, "RED"])), applied: {} };
}

export function fence(controller, generation) {
  if (!Number.isSafeInteger(generation) || generation < 0) throw new Error("Invalid generation");
  if (generation > controller.generation) {
    controller.generation = generation;
    controller.applied = {};
  }
}

export function apply(controller, command, phases, now = Date.now()) {
  if (command.generation < controller.generation) throw new Error("Superseded generation");
  if (!Number.isFinite(Date.parse(command.expires_at)) || Date.parse(command.expires_at) <= now) throw new Error("Expired command");
  if (!directions.includes(command.direction) || !["RED", "YELLOW", "GREEN"].includes(command.requested_state)) throw new Error("Invalid command");
  fence(controller, command.generation);
  if (controller.applied[command.command_id]) {
    if (controller.applied[command.command_id] !== command.requested_state) throw new Error("Conflicting command reuse");
    return controller.signals[command.direction];
  }
  const next = { ...controller.signals, [command.direction]: command.requested_state };
  const permissive = new Set();
  for (const direction of directions) {
    if (["GREEN", "YELLOW"].includes(next[direction])) {
      const phase = phases.find((p) => p.directions.includes(direction));
      if (!phase) throw new Error("Unknown movement");
      permissive.add(phase.id);
    }
  }
  if (permissive.size > 1) throw new Error("Physical conflict interlock");
  controller.signals = next;
  controller.applied[command.command_id] = command.requested_state;
  return next[command.direction];
}
