import { test } from "node:test";
import assert from "node:assert/strict";
import { apply, fence, initialController } from "./controller-model.mjs";

const phases = [{ id: "NS", directions: ["NORTH", "SOUTH"] }, { id: "EW", directions: ["EAST", "WEST"] }];
const command = (direction, requested_state, generation = 1) => ({ command_id: `${generation}-${direction}-${requested_state}`, direction, requested_state, generation, expires_at: new Date(10_000).toISOString() });

test("controller rejects conflicting permissive signals", () => {
  const state = initialController();
  apply(state, command("NORTH", "GREEN"), phases, 0);
  assert.throws(() => apply(state, command("EAST", "GREEN"), phases, 0), /interlock/);
  assert.equal(state.signals.EAST, "RED");
});

test("persisted fence rejects delayed old green after new red", () => {
  const state = initialController();
  apply(state, command("NORTH", "GREEN"), phases, 0);
  apply(state, command("NORTH", "RED", 2), phases, 0);
  const reconnected = JSON.parse(JSON.stringify(state));
  assert.throws(() => apply(reconnected, command("NORTH", "GREEN", 1), phases, 0), /Superseded/);
  assert.equal(reconnected.signals.NORTH, "RED");
});

test("expired commands cannot be executed", () => {
  assert.throws(() => apply(initialController(), command("EAST", "GREEN"), phases, 10_000), /Expired/);
});

test("same generation can confirm both compatible lamps and replay", () => {
  const state = initialController();
  for (const direction of ["NORTH", "SOUTH"]) apply(state, command(direction, "GREEN"), phases, 0);
  assert.equal(apply(state, command("NORTH", "GREEN"), phases, 0), "GREEN");
  fence(state, 3);
  assert.throws(() => apply(state, command("NORTH", "RED", 2), phases, 0), /Superseded/);
});
