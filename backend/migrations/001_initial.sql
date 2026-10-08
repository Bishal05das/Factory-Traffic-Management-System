CREATE TABLE IF NOT EXISTS schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE junctions (
    id text PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
    name text NOT NULL,
    policy_config jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE phases (
    junction_id text NOT NULL REFERENCES junctions(id),
    phase_id text NOT NULL,
    PRIMARY KEY (junction_id, phase_id)
);
CREATE TABLE directions (
    junction_id text NOT NULL REFERENCES junctions(id),
    direction text NOT NULL CHECK (direction IN ('NORTH','SOUTH','EAST','WEST')),
    PRIMARY KEY (junction_id, direction)
);
CREATE TABLE phase_members (
    junction_id text NOT NULL,
    phase_id text NOT NULL,
    direction text NOT NULL,
    PRIMARY KEY (junction_id, phase_id, direction),
    UNIQUE (junction_id, direction),
    FOREIGN KEY (junction_id, phase_id) REFERENCES phases(junction_id, phase_id),
    FOREIGN KEY (junction_id, direction) REFERENCES directions(junction_id, direction)
);

CREATE TABLE control_intents (
    id uuid PRIMARY KEY,
    junction_id text NOT NULL REFERENCES junctions(id),
    command text NOT NULL,
    requested_direction text,
    target_phase_id text,
    status text NOT NULL CHECK (status IN ('ACTIVE','ENDED')),
    accepted_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    UNIQUE (junction_id, id),
    FOREIGN KEY (junction_id, target_phase_id) REFERENCES phases(junction_id, phase_id),
    FOREIGN KEY (junction_id, requested_direction) REFERENCES directions(junction_id, direction)
);

CREATE TABLE command_batches (
    id uuid PRIMARY KEY,
    junction_id text NOT NULL REFERENCES junctions(id),
    generation bigint NOT NULL CHECK (generation > 0),
    purpose text NOT NULL,
    target_phase_id text,
    target_signals jsonb NOT NULL,
    status text NOT NULL CHECK (status IN ('PENDING','ACK','SUPERSEDED','TIMED_OUT')),
    issued_at timestamptz NOT NULL,
    deadline_at timestamptz NOT NULL,
    completed_at timestamptz,
    UNIQUE (junction_id, generation),
    UNIQUE (junction_id, id),
    FOREIGN KEY (junction_id, target_phase_id) REFERENCES phases(junction_id, phase_id)
);
CREATE UNIQUE INDEX one_pending_batch_per_junction ON command_batches(junction_id) WHERE status = 'PENDING';
CREATE INDEX batch_deadlines ON command_batches(status, deadline_at);

CREATE TABLE controller_commands (
    id uuid PRIMARY KEY,
    batch_id uuid NOT NULL,
    junction_id text NOT NULL,
    direction text NOT NULL,
    requested_state text NOT NULL CHECK (requested_state IN ('RED','YELLOW','GREEN')),
    status text NOT NULL CHECK (status IN ('PENDING','ACK','SUPERSEDED','TIMED_OUT')),
    acknowledged_at timestamptz,
    UNIQUE (batch_id, direction),
    UNIQUE (junction_id, id),
    FOREIGN KEY (junction_id, batch_id) REFERENCES command_batches(junction_id, id),
    FOREIGN KEY (junction_id, direction) REFERENCES directions(junction_id, direction)
);

CREATE TABLE junction_runtime (
    junction_id text PRIMARY KEY REFERENCES junctions(id),
    effective_mode text NOT NULL CHECK (effective_mode IN ('AUTOMATIC','MANUAL','EMERGENCY','FAILURE')),
    stage text NOT NULL CHECK (stage IN ('RECOVERING','WAIT_RED','ALL_RED_HOLD','WAIT_GREEN','GREEN','WAIT_YELLOW','YELLOW_HOLD','FAILURE_STOP')),
    current_phase_id text,
    target_phase_id text,
    generation bigint NOT NULL DEFAULT 0,
    revision bigint NOT NULL DEFAULT 0,
    active_batch_id uuid,
    active_manual_intent_id uuid,
    stage_deadline timestamptz,
    last_evaluated_at timestamptz NOT NULL,
    last_served jsonb NOT NULL DEFAULT '{}',
    fault text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (junction_id, current_phase_id) REFERENCES phases(junction_id, phase_id),
    FOREIGN KEY (junction_id, target_phase_id) REFERENCES phases(junction_id, phase_id),
    FOREIGN KEY (junction_id, active_batch_id) REFERENCES command_batches(junction_id, id),
    FOREIGN KEY (junction_id, active_manual_intent_id) REFERENCES control_intents(junction_id, id)
);

CREATE TABLE signal_states (
    junction_id text NOT NULL,
    direction text NOT NULL,
    desired_state text NOT NULL CHECK (desired_state IN ('RED','YELLOW','GREEN')),
    actual_state text NOT NULL CHECK (actual_state IN ('RED','YELLOW','GREEN','UNKNOWN')),
    confirmed_command_id uuid,
    confirmed_at timestamptz,
    PRIMARY KEY (junction_id, direction),
    FOREIGN KEY (junction_id, direction) REFERENCES directions(junction_id, direction),
    FOREIGN KEY (junction_id, confirmed_command_id) REFERENCES controller_commands(junction_id, id),
    CHECK ((actual_state = 'UNKNOWN' AND confirmed_at IS NULL) OR (actual_state <> 'UNKNOWN' AND confirmed_at IS NOT NULL))
);
CREATE TABLE devices (
    junction_id text NOT NULL REFERENCES junctions(id),
    device_key text NOT NULL,
    device_type text NOT NULL CHECK (device_type IN ('SIGNAL_CONTROLLER','SENSOR','SIGNAL')),
    direction text,
    status text NOT NULL CHECK (status IN ('ONLINE','OFFLINE','DEGRADED','WARNING','UNKNOWN')),
    last_reported_at timestamptz,
    PRIMARY KEY (junction_id, device_key),
    FOREIGN KEY (junction_id, direction) REFERENCES directions(junction_id, direction)
);

CREATE TABLE sensor_events (
    event_id text PRIMARY KEY,
    junction_id text REFERENCES junctions(id),
    claimed_junction_id text NOT NULL,
    direction text,
    vehicle_id text,
    event_type text,
    sequence_no bigint,
    payload_hash text NOT NULL,
    canonical_payload jsonb NOT NULL,
    outcome text NOT NULL,
    result jsonb NOT NULL DEFAULT '{}',
    sensor_at timestamptz,
    received_at timestamptz NOT NULL,
    accepted_at timestamptz
);
CREATE TABLE vehicle_trackers (
    junction_id text NOT NULL,
    direction text NOT NULL,
    vehicle_id text NOT NULL,
    last_sequence_no bigint NOT NULL CHECK (last_sequence_no > 0),
    last_event_id text NOT NULL REFERENCES sensor_events(event_id),
    last_event_type text NOT NULL,
    PRIMARY KEY (junction_id, direction, vehicle_id),
    FOREIGN KEY (junction_id, direction) REFERENCES directions(junction_id, direction)
);
CREATE TABLE queue_entries (
    junction_id text NOT NULL,
    direction text NOT NULL,
    vehicle_id text NOT NULL,
    vehicle_type text NOT NULL CHECK (vehicle_type IN ('FORKLIFT','TRUCK','EMPLOYEE_VEHICLE','EMERGENCY')),
    arrival_event_id text NOT NULL REFERENCES sensor_events(event_id),
    accepted_at timestamptz NOT NULL,
    emergency_expires_at timestamptz,
    emergency_priority_active boolean NOT NULL,
    PRIMARY KEY (junction_id, direction, vehicle_id),
    UNIQUE (junction_id, vehicle_id),
    FOREIGN KEY (junction_id, direction, vehicle_id) REFERENCES vehicle_trackers(junction_id, direction, vehicle_id)
);
CREATE INDEX queue_waiting ON queue_entries(junction_id, direction, accepted_at);

CREATE TABLE controller_feedback (
    id bigserial PRIMARY KEY,
    command_id uuid,
    claimed_command_id text NOT NULL,
    junction_id text REFERENCES junctions(id),
    claimed_junction_id text NOT NULL,
    status text NOT NULL,
    actual_state text NOT NULL,
    outcome text NOT NULL,
    payload jsonb NOT NULL,
    controller_at timestamptz,
    received_at timestamptz NOT NULL,
    FOREIGN KEY (junction_id, command_id) REFERENCES controller_commands(junction_id, id)
);
CREATE TABLE device_events (
    event_id text PRIMARY KEY,
    junction_id text REFERENCES junctions(id),
    claimed_junction_id text NOT NULL,
    device_key text NOT NULL,
    payload_hash text NOT NULL,
    payload jsonb NOT NULL,
    outcome text NOT NULL,
    result jsonb NOT NULL DEFAULT '{}',
    device_at timestamptz,
    received_at timestamptz NOT NULL,
    accepted_at timestamptz
);
CREATE TABLE alerts (
    junction_id text NOT NULL REFERENCES junctions(id),
    code text NOT NULL,
    severity text NOT NULL DEFAULT 'WARNING',
    message text NOT NULL,
    opened_at timestamptz NOT NULL,
    resolved_at timestamptz,
    PRIMARY KEY (junction_id, code, opened_at)
);
CREATE UNIQUE INDEX active_alerts ON alerts(junction_id, code) WHERE resolved_at IS NULL;
CREATE TABLE audit_log (
    id bigserial PRIMARY KEY,
    junction_id text REFERENCES junctions(id),
    junction_revision bigint NOT NULL DEFAULT 0,
    event_type text NOT NULL,
    direction text,
    sensor_event_id text,
    command_id uuid,
    intent_id uuid,
    details jsonb NOT NULL DEFAULT '{}',
    occurred_at timestamptz NOT NULL,
    FOREIGN KEY (junction_id, command_id) REFERENCES controller_commands(junction_id, id),
    FOREIGN KEY (junction_id, intent_id) REFERENCES control_intents(junction_id, id)
);
CREATE INDEX junction_history ON audit_log(junction_id, id DESC);
