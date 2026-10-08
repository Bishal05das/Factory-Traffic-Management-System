# API reference

The Go API uses JSON. Default base URL: `http://localhost:8080`; the frontend proxies the same `/api` paths through its own origin. Substitute the host port configured in `.env`. Responses are not cached.

Mutations are serialized per junction. A successful operation can be pending physical execution; clients must read status or consume controller commands/feedback to observe confirmation. Clients cannot write arbitrary signal states.

## Junctions

`GET /api/junctions` returns an array of status snapshots. `GET /api/junctions/{id}` returns configuration. `GET /api/junctions/{id}/status` returns one transaction-consistent snapshot.

`POST /api/junctions` creates a junction with explicit configuration and returns 201. Its initial physical state is UNKNOWN and desired signals are RED. Example:

```json
{
  "id": "B",
  "name": "Junction B",
  "phases": [
    {"id": "NORTH_SOUTH", "directions": ["NORTH", "SOUTH"]},
    {"id": "EAST_WEST", "directions": ["EAST", "WEST"]}
  ],
  "policy": {
    "green_seconds": 30,
    "yellow_seconds": 5,
    "clearance_seconds": 2,
    "ack_seconds": 5,
    "manual_seconds": 120,
    "emergency_seconds": 120,
    "starvation_seconds": 120,
    "age_step_seconds": 10,
    "weights": {"TRUCK": 3, "FORKLIFT": 2, "EMPLOYEE_VEHICLE": 1, "EMERGENCY": 3}
  }
}
```

Identifiers are 1–128 characters from letters, digits, `.`, `_`, and `-`. Phases must have unique IDs and assign each of the four directions exactly once to the specified compatible pair. Durations must be explicit positive seconds, at most 86400. Normal weights are integers 1–1000 preserving TRUCK > FORKLIFT > EMPLOYEE_VEHICLE; expired EMERGENCY weight must match TRUCK. Duplicate junction IDs return 409.

Status includes:

| Field | Meaning |
| --- | --- |
| `junction_id`, `name`, `config` | Junction and its immutable first-version configuration |
| `revision`, `generation` | Committed state revision and controller fencing generation |
| `mode` | AUTOMATIC, MANUAL, EMERGENCY, FAILURE |
| `stage` | RECOVERING, WAIT_RED, ALL_RED_HOLD, WAIT_GREEN, GREEN, WAIT_YELLOW, YELLOW_HOLD, FAILURE_STOP |
| `phase`, `target_phase` | Current transition/service phase and latest selected intent |
| `desired_signals` | Backend-requested states by direction |
| `actual_signals`, `signal_evidence` | Trusted confirmed states, command correlation, and time; UNKNOWN when unverified |
| `queues`, `vehicles` | Counts derived from queued vehicles and their waiting metadata |
| `emergencies`, `manual` | Active emergency priority and stored manual intent |
| `controller_status`, `devices`, `alerts` | Device health and active alerts |
| `pending_batch` | Current batch awaiting ACK, or null; individually acknowledged commands remain in the batch |
| `transition_deadline` | Pending ACK deadline or timed stage deadline, or null |
| `sequence_high_water` | Largest retained vehicle sequence per direction, useful for simulation |

An empty `phase` does not prove physical all-red: inspect `actual_signals` and `stage`. A desired/actual mismatch in a pending transition means confirmation is outstanding; a fault produces alerts/failure and may invalidate evidence.

## Vehicle events

`POST /api/sensor-events`

```json
{
  "event_id": "evt-10001",
  "junction_id": "A",
  "direction": "NORTH",
  "event_type": "VEHICLE_ARRIVED",
  "vehicle_id": "VH-501",
  "vehicle_type": "TRUCK",
  "sequence_no": 1501,
  "timestamp": "2026-10-08T10:15:20Z"
}
```

Clearance:

```json
{
  "event_id": "evt-10002",
  "junction_id": "A",
  "direction": "NORTH",
  "event_type": "VEHICLE_CLEARED",
  "vehicle_id": "VH-501",
  "sequence_no": 1502,
  "timestamp": "2026-10-08T10:16:10Z"
}
```

Supported types: TRUCK, FORKLIFT, EMPLOYEE_VEHICLE, EMERGENCY. Arrival requires a vehicle type; clearance can omit it. Require a positive integer sequence and nonzero RFC 3339 timestamp. A clear event with no matching arrival creates a tombstone but no negative count.

New accepted events return 201:

```json
{"outcome":"accepted","event_id":"evt-10001","revision":16}
```

Exact canonical replay returns 200 with `outcome: "duplicate"` and the originally recorded result/revision. Whitespace, JSON field order, and equivalent UTC instants do not change identity. Reusing an ID for a different canonical payload returns 409. A stale/reused per-vehicle sequence, arrival for an already queued vehicle, or cross-direction queuing returns 409 and is audited. Replaying a previously rejected event retains its rejection.

The backend accepts delayed source timestamps as metadata. It orders control decisions by transaction acceptance time and enforces sequence order per `(junction, direction, vehicle)`; it does not reject all older events solely because another vehicle's sequence is larger.

## Manual and recovery commands

`POST /api/junctions/{id}/commands`

```json
{"command":"MANUAL_GREEN_REQUEST","direction":"WEST"}
```

```json
{"command":"RETURN_TO_AUTOMATIC"}
```

```json
{"command":"RECOVER"}
```

Accepted intent returns 202 with `outcome: "pending_physical_confirmation"` and revision. Same-phase intent can need no new signal command; 202 represents asynchronous control acceptance, not a guarantee that lamps changed.

Manual requests expire after the configured lifetime. They are rejected during unresolved startup/fault recovery or unhealthy device state. Emergency priority temporarily supersedes manual intent. RECOVER is allowed only after a fault or during startup reconciliation, with all nine configured device health reports ONLINE. ONLINE alone never resumes a stopped junction.

These operations do not have client idempotency keys. Do not automatically retry a manual command after an ambiguous network failure; check status/history first. Multiple new accepted manual requests are distinct intents.

## Controller commands and feedback

`GET /api/junctions/{id}/controller-commands` returns only committed commands still awaiting feedback:

```json
{
  "junction_id":"A",
  "generation":5,
  "commands":[
    {
      "command_id":"00000000-0000-4000-8000-000000000001",
      "batch_id":"00000000-0000-4000-8000-000000000002",
      "junction_id":"A",
      "generation":5,
      "direction":"EAST",
      "requested_state":"GREEN",
      "status":"PENDING",
      "issued_at":"2026-10-08T10:20:00Z",
      "expires_at":"2026-10-08T10:20:05Z"
    }
  ]
}
```

A controller must fence older generations, reject expired commands, preserve fencing through reconnection, enforce a conflict interlock, and report actual physical execution. A command feed read is not a physical acknowledgement.

`POST /api/controller-events` correlates feedback:

```json
{
  "command_id":"00000000-0000-4000-8000-000000000001",
  "junction_id":"A",
  "status":"ACK",
  "actual_state":"GREEN",
  "timestamp":"2026-10-08T10:20:01Z"
}
```

Use ACK or NACK; actual_state is RED, YELLOW, GREEN, or UNKNOWN. Timestamp is optional but, if supplied, must be valid and nonzero. Direction is resolved from the command. The PDF's direction-less acknowledgement shape is supported; no arbitrary uncorrelated signal write is accepted.

Recorded feedback returns 200 with outcome `accepted`, `duplicate`, `stale`, `late`, or `fault`. A matching ACK for an already acknowledged command is an idempotent semantic replay. A differing state is a fault report. Partial batches cannot advance the stage; old or expired ACKs cannot satisfy the current batch. Late/conflicting feedback can put the junction in failure despite the HTTP receipt being successful. A command from another junction or an unknown command returns 404.

## Device health

`POST /api/device-events` is an added endpoint separating device health from command acknowledgement.

```json
{
  "event_id":"status-301",
  "junction_id":"A",
  "device_type":"SIGNAL_CONTROLLER",
  "status":"OFFLINE",
  "timestamp":"2026-10-08T10:30:00Z"
}
```

For `SENSOR` and `SIGNAL`, include a supported `direction`. `SIGNAL_CONTROLLER` is junction-wide; an optional valid direction is accepted for compatibility with the PDF example and does not narrow its scope. Status: ONLINE, OFFLINE, DEGRADED, WARNING, UNKNOWN. New accepted device events return 201; identical device-ID replays return 200; conflicting reuse returns 409.

There is no heartbeat expiry in the REST assessment. Device health starts UNKNOWN and must be re-announced after backend restart. Any non-ONLINE configured device stops green grants; OFFLINE does not remove persisted traffic.

## History and health

`GET /api/junctions/{id}/history?limit=50&before=1234` returns newest-first audit entries. Limit is 1–200; omit `before` or use 0 for latest. Use the oldest returned ID as the next `before` cursor. History records vehicle changes, replay/rejection, transitions, desired/confirmed states, emergencies, overrides, mode changes, device status, timeout, and restart/recovery. Global malformed/unknown-junction rejection entries remain in the database but are not returned by a junction-specific history query.

`GET /health/live` reports process liveness. `GET /health/ready` checks database availability; this is API readiness, not physical-junction readiness. Inspect junction status for physical safety/recovery state. Ownership or scheduler continuity loss shuts down the backend so Docker can restart and reconcile it.

## Errors

```json
{"error":"conflict","message":"event_id was already used for a different payload"}
```

| HTTP code | Meaning |
| --- | --- |
| 400 | Malformed JSON, unknown fields, oversized/invalid JSON decoding, or multiple JSON values |
| 404 | Unknown junction or command |
| 409 | Conflicting identity, ordering, queue state, unhealthy control/recovery, or existing junction |
| 422 | Well-formed request with unsupported/missing domain values or invalid configuration |
| 503 | Persistence/backend unavailable; frontend proxy also uses this for backend connectivity failure |

Go limits JSON request bodies to 64 KiB and uses bounded request contexts. No production authentication or MQTT endpoint is provided in this assessment.
