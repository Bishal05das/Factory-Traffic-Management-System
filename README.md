# Factory Traffic Management System

A small event-driven traffic controller for factory roads, built with plain Go (`net/http`), Next.js, PostgreSQL, and Docker. Junction A is seeded with NORTH/SOUTH and EAST/WEST phases; additional junctions use the same engine.

The backend owns queues, scheduling, safe transitions, device evidence, and recovery. The dashboard displays backend decisions and submits intents. Controller commands remain pending until correlated physical acknowledgements arrive.

## Run with Docker

Requirements: Docker Engine and Docker Compose. Node.js and Go are not required for the containerized application.

```bash
cp .env.example .env
docker compose --profile simulation up --build -d --wait
```

Open the dashboard at **http://localhost:3000** and Junction A at **http://localhost:3000/junctions/A**. The API is available at **http://localhost:8080/api/junctions/A/status**. PostgreSQL is private to the Compose network.

The `simulation` profile explicitly enables a standalone REST controller simulator. It announces simulated device health, applies committed commands with expiry/generation checks, and sends acknowledgements. The default initial state is unverified all-red intent; fresh red confirmation is required before any green grant.

Change `WEB_PORT` and `API_PORT` in `.env` if ports are occupied. In the development workspace used for verification, these are **13000** and **18080**, respectively. The dashboard there is **http://localhost:13000**. The API connection inside Docker remains `http://backend:8080` regardless of host port changes.

```bash
docker compose --profile simulation ps
docker compose logs --tail=50 backend frontend simulator
docker compose --profile simulation stop
docker compose --profile simulation up -d --wait
```

Named volumes preserve PostgreSQL data and simulator fencing across container restarts. Do not remove those volumes when demonstrating persistence. The demo password in `.env.example` is for the local assessment. If changing it, use URI-safe characters because Compose interpolates it into `DATABASE_URL`; changing an existing database's password also requires updating that database, not just its environment variable.

## Run services locally

Use Go 1.24 or newer and Node.js 22 or newer. PostgreSQL must already be reachable.

```bash
cd backend
export DATABASE_URL='postgres://traffic:your_password@localhost:5432/factory_traffic?sslmode=disable'
go run ./cmd/server
```

In another terminal:

```bash
cd frontend
npm ci
BACKEND_URL=http://localhost:8080 npm run dev
```

To enable automatic controller simulation outside Docker:

```bash
API_URL=http://localhost:8080 node tools/controller-simulator.mjs
```

The simulator's `.simulator-state.json` is ignored by Git. Preserve it when restarting the simulator; a controller that forgets its fence is a different physical-controller session requiring reconciliation.

## Architecture

[ARCHITECTURE.md](ARCHITECTURE.md) contains the component diagram, Docker topology, state-machine diagram, emergency sequence, ERD, transaction strategy, recovery contract, and branch workflow. Keep it current when implementation changes.

The Go domain package has no HTTP, SQL, MQTT, or browser dependencies. Application services execute short transactions. PostgreSQL row locks serialize writes to each junction, while a dedicated advisory lock prevents two backend processes from independently coordinating the controllers. The REST adapter exposes only committed controller commands.

Persistence includes junction/phase configuration, runtime deadlines and generations, directional signal evidence, device health, event deduplication results, vehicle trackers and clearance tombstones, queues, manual intents, command batches, feedback, alerts, and immutable audit entries. Versioned migrations are embedded into the server and applied at startup.

## Scheduling

Normal phase score is the sum, per waiting vehicle, of its weight plus server-measured waiting seconds divided by 10:

```text
TRUCK = 3       FORKLIFT = 2       EMPLOYEE_VEHICLE = 1
EMERGENCY after priority expiry = TRUCK weight
```

Active emergencies take precedence over manual and automatic scheduling. Competing emergencies use earliest server acceptance, then arrival event ID. A manual intent chooses the whole compatible phase and lasts 120 seconds; the last serialized manual request replaces its predecessor. After an emergency ends, an unexpired manual intent resumes.

Automatic mode respects a 30-second confirmed green duration before switching to conflicting traffic. With no competing demand, it retains the phase. After 120 seconds of unserved demand, oldest unserved traffic takes precedence at the next safe automatic selection point. A continuously green phase is currently being served, even if its vehicles have not sent clearance events. Its latest service time is persisted when yellow begins, so it cannot defeat starvation protection during all-red reselection. Normal score ties favor the current phase, then oldest arrival, then stable phase ID.

Manual control, ongoing emergencies, and failures can delay ordinary traffic beyond the starvation threshold; it is not an unconditional maximum waiting guarantee. A green grant does not remove vehicles from the queue.

## Signal transitions and failures

```text
Confirmed GREEN
  → request outgoing YELLOW → complete ACKs → hold YELLOW for 5 seconds
  → request ALL RED → complete ACKs → hold ALL RED for 2 seconds
  → re-evaluate current intent → request target GREEN → complete ACKs
```

Timers start only after the whole command batch is confirmed. Partial ACKs update individual lamp evidence without advancing the stage. Incoming intents cannot cancel a yellow/clearance hold. Emergency arrival during a pending green waits for that batch to establish its physical phase, then immediately begins safe yellow preemption.

Commands expire after 5 seconds. NACK, timeout, mismatch, or unavailable devices stop new green grants and request all-red when controller communication permits. Physical state becomes UNKNOWN when continuity is lost. An ONLINE report is not a lamp confirmation. Fault recovery requires healthy devices and an explicit `RECOVER` intent, followed by a fresh all-red batch and clearance interval.

On backend restart, queues, deduplication, history, and unexpired intents survive. Pending commands are superseded, generation fencing advances, old stage timers are discarded, and physical/device evidence becomes UNKNOWN. Startup reconciliation requires fresh health and all-red confirmation; it never resumes a saved GREEN as if physical state were known.

## API and demonstration

- [API reference](docs/API.md): endpoints, request/response examples, validation, idempotency, and status codes.
- [Demonstration guide](docs/DEMO.md): all nine assessment scenarios, controller failure controls, restart, and video walkthrough.

The dashboard polls every second with cancellation and backoff. It retains the last successful snapshot with a stale-data warning on failures, disables stale control actions, and distinguishes confirmed all-red from unknown physical state.

## Verification

Domain tests and Go static checks:

```bash
cd backend
go test -race ./...
go vet ./...
```

Database/API integration tests require `TEST_DATABASE_URL`. Without it, those integration tests explicitly skip; the domain tests still run. Use an isolated test database, not the demonstration database.

For a fresh local test container:

```bash
docker run -d --name ftms-assessment-test-db \
  --label factorytraffic.purpose=integration-test \
  -e POSTGRES_USER=traffic -e POSTGRES_PASSWORD=traffic_test_only \
  -e POSTGRES_DB=traffic_test -p 127.0.0.1:55439:5432 postgres:17-alpine
```

If that container already exists, use `docker start ftms-assessment-test-db` instead. Wait for `docker exec ftms-assessment-test-db pg_isready -U traffic -d traffic_test` to report ready, then:

```bash
cd backend
TEST_DATABASE_URL='postgres://traffic:traffic_test_only@localhost:55439/traffic_test?sslmode=disable' go test -race ./...
```

Frontend and simulator checks:

```bash
cd frontend
npm ci
npm run check
npm run build
cd ..
node tools/controller-model.test.mjs
docker compose --profile simulation config --quiet
```

Full HTTP scenarios, with controlled ACKs and an actual backend restart:

```bash
docker compose --profile simulation stop simulator
API_URL=http://localhost:8080 node tools/scenarios.mjs --restart
docker compose --profile simulation up -d simulator
```

Adjust `API_URL` to the configured API port (18080 in the verified workspace). Run this from the repository root. The script creates its own verification junction with explicitly short test durations; Junction A's 30/5/2-second policy is unchanged. It clears its traffic on completion and retains the junction/history as evidence. It retries only read observations during restart, never control mutations.

Verified during development: race-enabled domain/PostgreSQL/API tests, `go vet`, frontend type check and production build, controller interlock/fencing tests, Docker image builds and readiness, full HTTP scenarios including actual restart, and Chrome checks for arrival/replay/clearance/manual operations, invalid junction, desktop rendering, and a 375-pixel mobile viewport.

## Git workflow

Feature branches contain meaningful incremental commits and merge into `main` when their checks pass. `pre-release` is cut after MVP integration and receives integration corrections and submission docs through focused feature branches. `release/v1.0.0` is cut from verified `pre-release`; use that branch for the demo/deployment. Bring the verified integration fixes back into `main`.

Commit messages follow `<type>(<scope>): <short description>` using `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, or `build`. History includes actual regression reproduction and correction rather than invented development steps.

## Assumptions / Questions / Requirement Issues

The user confirmed the timing, priority, recovery, and simulator policies before implementation. Significant requirement decisions are:

| Issue | Implemented decision |
| --- | --- |
| Compatibility / turning movements | Only NORTH/SOUTH and EAST/WEST; a directional manual request serves its compatible phase |
| Event ID authority | Globally unique within sensor events; device events use a separate namespace; same canonical payload is idempotent, different payload reuse is a conflict |
| Sensor sequence scope | One logical sensor stream per direction; enforce per-vehicle high-water marks so reordered events for different vehicles are not discarded |
| Source versus server timestamp | Source time is audit metadata; serialized server acceptance determines waiting/order/timeouts; old source time alone is not rejected |
| Clearance before arrival | Persist an ordering tombstone and warning without subtracting from an absent queue |
| Already queued arrival / direction movement | Reject a second distinct arrival; clear a vehicle before queuing it in another direction at the same junction |
| Manual lifetime / administrator disconnect | 120-second expiry, explicit automatic return, serialized replacement; disconnect does not change lifetime |
| Emergency completion / stale request | Clearance ends priority; 120-second expiry retains the vehicle but reduces priority to truck weight and raises an alert |
| Multiple emergencies | Earliest accepted emergency phase wins; compatible emergencies share phase service; later conflicting emergency waits for completion/expiry |
| Controller execution | Sending is not success; batch-correlated ACKs, timeout, desired/actual separation, and no automatic retry |
| Physical fencing | Simulator persists generations and rejects expired/older commands; real hardware must provide equivalent ordering and conflict interlocks |
| Device failure / reconnection | Any offline configured sensor/signal/controller stops green grants; ONLINE does not clear faults; explicit recovery requires fresh red evidence |
| Fault stop versus normal yellow | Faults may request immediate all-red as a stop action; no conflicting green follows until reconciliation; real hardware must define its local fault-stop sequence |
| Queue starvation | Protect time without phase service, not merely oldest uncleared arrival; continuously served traffic cannot monopolize the oldest-arrival rule |
| Restart during transition | Supersede pending work, invalidate evidence, preserve traffic/intent/history, and reconcile through fresh all-red |
| Malformed / unknown-junction events | Persist rejection audit without queue mutation; global rejection entries are stored in PostgreSQL but are not exposed by junction-specific history |
| Device heartbeat missing | REST demonstration relies on explicit status events and command timeouts; physical heartbeat/watchdog behavior remains an integration contract |
| Controller owner | One active backend, enforced with an advisory lock; fail-stop the process if its coordination/persistence continuity is lost |
| Authentication | Assessment-local administrator and device simulation APIs; production authentication was not required and is not implemented |

## Limits and next work

This is a REST demonstration, not a hardware safety certification. An offline physical controller cannot be made safe by changing database rows. Real hardware integration needs persistent command fencing, trustworthy physical feedback, a local conflict interlock/watchdog, device heartbeat/session ordering, and agreement on fault-stop timings.

MQTT, authentication/authorization, multi-instance leadership, analytics, event replay, and data-retention rules are not implemented. The small-junction repository atomically rewrites the active queue projection on a state-changing transaction; a larger installation should use incremental writes and bounded aggregate loading. History has cursor pagination; queue, tracker, and event-history growth currently has no retention horizon.

## AI / Tool Usage

OpenAI Codex assisted with reading the assessment PDF, architecture and ERD design, implementation, regression analysis, tests, Git commits, and submission documentation. Terminal tools, Go tests/race detection, PostgreSQL in Docker, npm/Next.js builds, and headless Chrome were used for verification. Operating-policy choices were confirmed by the user. The source and documentation are intended to be reviewed and understood by the submitting candidate.
