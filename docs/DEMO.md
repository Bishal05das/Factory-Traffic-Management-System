# Demonstration guide

Use `release/v1.0.0` for the recorded video or local deployment. Run from the repository root. Replace default API/dashboard ports with values in `.env` (18080/13000 in the verified workspace).

## Start and observe

```bash
docker compose --profile simulation up --build -d --wait
```

Open `/junctions/A` on the dashboard. The automatic simulator announces all nine devices ONLINE, confirms a fresh all-red batch, and begins the two-second clearance hold. No queued demand means it stays red. The simulator is independent of browser connectivity.

The large lamps show controller-confirmed physical state; the small requested indicators show desired state. Mode describes priority, stage describes transition progress. UNKNOWN means physical state has not been verified. Controller ONLINE is connectivity/health, not proof of lamp execution.

## Nine assessment scenarios

### 1. Normal traffic

In Traffic Simulation, add arrivals with distinct vehicle IDs from NORTH and EAST. Watch queues and the selected phase. NORTH/SOUTH and EAST/WEST are served as compatible pairs. A first green follows complete red confirmation and clearance. Normal switches wait for the configured 30-second confirmed green period.

Clear each vehicle using its queue-row action when it has crossed. Signals changing green do not automatically clear queues.

### 2. Priority traffic

Begin with no active emergency or manual intent. Add one NORTH TRUCK and one EAST EMPLOYEE_VEHICLE before the next phase decision. Truck weight is 3 versus employee weight 1; forklift weight is 2. Waiting age and starvation also affect the choice, so reset previous queue demand with clearance events or use a fresh junction to make the comparison obvious.

### 3. Emergency preemption

Establish NORTH/SOUTH GREEN with a normal arrival. Select EAST in the simulation form and click **Simulate emergency**. Observe EMERGENCY mode and the outgoing phase's requested yellow while EAST remains red. After yellow ACKs and the five-second hold, all-red is requested; only after complete red ACKs and the two-second clearance may EAST/WEST turn green.

Clear the emergency using its queue-row action. Its queue entry remains visible until clearance even when green. Compatible emergencies share service; conflicting emergencies use earliest acceptance and cannot bypass clearance.

### 4. Manual override

Click a manual direction button and observe MANUAL mode, the active intent, and its expiry. The backend safely moves to that direction's compatible phase. Click **Return to automatic**. An emergency supersedes manual service while retaining any unexpired manual intent; expiry is not tied to the browser remaining connected.

### 5. Duplicate event

Submit an arrival, note its queue count, and click **Replay last event**. The result reports Duplicate; no vehicle is added a second time. Expand **Last event payload** to show that the same event_id and payload were sent. History records the duplicate receipt.

Changing a payload while reusing its event ID returns a conflict rather than changing the original event.

### 6. Vehicle clearance

Click **Clear vehicle** on a queued item. The client fetches the backend sequence high-water mark before creating a newer clearance event. Count decreases without becoming negative. Replay the clearance to show idempotency. A clearance for an absent vehicle records a tombstone so a delayed older arrival cannot re-create it.

### 7. Controller failure

To control acknowledgements manually, first stop the automatic simulator after healthy startup:

```bash
docker compose --profile simulation stop simulator
```

Request a conflicting manual phase. Pending command rows appear. Confirm individual commands or **Acknowledge entire batch** to demonstrate partial/complete confirmation. If the five-second ACK deadline is missed, the backend enters FAILURE_STOP, desired signals are all-red, actual evidence is UNKNOWN, and timeout activity is recorded.

Alternatively, report a controller OFFLINE, a signal failure, or a directional sensor OFFLINE in the device form. The junction stops new green grants. ONLINE alone does not resume it. Set all devices back ONLINE, click **Start safe recovery**, then acknowledge a fresh all-red batch. Timed clearance must finish before any green grant.

Resume automatic simulation when finished:

```bash
docker compose --profile simulation up -d simulator
```

The simulator respects explicit OFFLINE reports; it does not silently change them to ONLINE. It announces UNKNOWN device health after a backend restart because these are simulated devices whose health it knows.

### 8. Restart

Add vehicles and optionally an active manual/emergency intent. Record queue counts and history. Restart the backend:

```bash
docker compose restart backend
```

Queues, event identity, unexpired intents, and audit history remain. Physical confirmations and device health are invalidated. With automatic simulation enabled, fresh ONLINE and all-red ACKs arrive quickly; inspect restart/recovery history to see the reconciliation. To observe UNKNOWN long enough for a screenshot, stop the simulator before restarting. The junction remains RECOVERING until health and fresh red evidence return.

### 9. Concurrent events

The automated scenario script submits simultaneous vehicle arrivals, a conflicting emergency, a manual intent, a duplicate emergency event, and an ACK. It asserts one queue entry per unique arrival and checks desired/actual phase compatibility. PostgreSQL/API integration tests also use concurrent requests and Go's race detector.

## Automated walkthrough

```bash
docker compose --profile simulation stop simulator
API_URL=http://localhost:8080 node tools/scenarios.mjs --restart
docker compose --profile simulation up -d simulator
```

Use `http://localhost:18080` on the verified workspace. The script creates a `CHECK-*` junction with short explicit test timings, performs the scenarios, restarts the actual Compose backend, verifies persistence, clears its test vehicles, and retains history. It does not alter Junction A's normal timing policy. Other traffic at existing junctions also undergoes normal restart reconciliation when `--restart` is used.

## Suggested video order

1. Show `git branch --show-current` and the release commit; show the architecture diagram and ERD.
2. Show overview, desired versus actual signals, device health, and queue simulation.
3. Demonstrate duplicate replay and vehicle clearance.
4. Demonstrate emergency preemption and manual return.
5. Demonstrate missing ACK/offline status and explicit recovery.
6. Show restart persistence and audit entries, then the passing automated checks and meaningful Git history.

Use normal timings for the main Junction A demonstration; explain any short test configuration shown by automated verification. A local software demo does not claim an offline physical controller obeyed an all-red request.
