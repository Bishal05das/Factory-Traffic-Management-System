package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"factorytraffic/internal/domain"
	"factorytraffic/internal/ports"
	"factorytraffic/migrations"
	"github.com/lib/pq"
)

type Store struct{ DB *sql.DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

func (s *Store) Ping(ctx context.Context) error { return s.DB.PingContext(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('factorytraffic-migrations'))"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())"); err != nil {
		return err
	}
	files, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, file := range files {
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", file.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, readErr := migrations.Files.ReadFile(file.Name())
		if readErr != nil {
			return readErr
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", file.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", file.Name()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// A dedicated connection owns the advisory lock for the backend's lifetime.
// Row locks serialize traffic operations; this lock prevents a second process
// from invalidating controller evidence through independent startup recovery.
func (s *Store) AcquireOwnership(ctx context.Context) (*sql.Conn, error) {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var acquired bool
	err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext('factorytraffic-controller-owner'))").Scan(&acquired)
	if err != nil || !acquired {
		conn.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("another backend already owns controller coordination")
	}
	return conn, nil
}

func (s *Store) Create(ctx context.Context, c domain.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	policy, err := json.Marshal(c.Policy)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO junctions(id,name,policy_config) VALUES($1,$2,$3)", c.ID, c.Name, string(policy)); err != nil {
		return translate(err)
	}
	for _, phase := range c.Phases {
		if _, err = tx.ExecContext(ctx, "INSERT INTO phases(junction_id,phase_id) VALUES($1,$2)", c.ID, phase.ID); err != nil {
			return err
		}
		for _, d := range phase.Directions {
			if _, err = tx.ExecContext(ctx, "INSERT INTO directions(junction_id,direction) VALUES($1,$2)", c.ID, d); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO phase_members(junction_id,phase_id,direction) VALUES($1,$2,$3)", c.ID, phase.ID, d); err != nil {
				return err
			}
		}
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return err
	}
	state, err := domain.NewState(c, now)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO junction_runtime(junction_id,effective_mode,stage,last_evaluated_at) VALUES($1,$2,$3,$4)", c.ID, state.Runtime.Mode, state.Runtime.Stage, now); err != nil {
		return err
	}
	u := &unit{tx: tx, id: c.ID, now: now}
	effects := domain.Effects{Audit: []domain.Audit{{Type: "JUNCTION_CREATED", At: now}}}
	if err = u.Save(ctx, state, effects); err != nil {
		return err
	}
	return tx.Commit()
}

func translate(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrNotFound
	}
	var pgerr *pq.Error
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return ports.ErrExists
	}
	return err
}

func (s *Store) List(ctx context.Context) ([]domain.Config, error) {
	// Configuration is immutable in the first version, so this list can load each
	// configuration without holding a transaction across API calls.
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM junctions ORDER BY id")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	configs := []domain.Config{}
	for _, id := range ids {
		state, readErr := s.Read(ctx, id)
		if readErr != nil {
			return nil, readErr
		}
		configs = append(configs, state.Config)
	}
	return configs, nil
}

func (s *Store) Read(ctx context.Context, id string) (*domain.State, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	state, err := load(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *Store) Transact(ctx context.Context, id string, fn func(*domain.State, ports.Transaction) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var locked string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM junctions WHERE id=$1 FOR UPDATE", id).Scan(&locked); err != nil {
		return translate(err)
	}
	state, err := load(ctx, tx, id)
	if err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return err
	}
	if now.Before(state.Runtime.LastEvaluatedAt) {
		now = state.Runtime.LastEvaluatedAt
	}
	if err = fn(state, &unit{tx: tx, id: id, now: now}); err != nil {
		return err
	}
	return tx.Commit()
}

func load(ctx context.Context, tx *sql.Tx, id string) (*domain.State, error) {
	c := domain.Config{ID: id}
	var policy []byte
	if err := tx.QueryRowContext(ctx, "SELECT name,policy_config FROM junctions WHERE id=$1", id).Scan(&c.Name, &policy); err != nil {
		return nil, translate(err)
	}
	if err := json.Unmarshal(policy, &c.Policy); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT phase_id,array_agg(direction ORDER BY direction) FROM phase_members WHERE junction_id=$1 GROUP BY phase_id ORDER BY phase_id", id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p domain.Phase
		var directions pq.StringArray
		if err = rows.Scan(&p.ID, &directions); err != nil {
			rows.Close()
			return nil, err
		}
		for _, d := range directions {
			p.Directions = append(p.Directions, domain.Direction(d))
		}
		c.Phases = append(c.Phases, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	state, err := domain.NewState(c, time.Time{})
	if err != nil {
		return nil, err
	}
	var current, target, batchID, manualID sql.NullString
	var deadline sql.NullTime
	var lastServed []byte
	r := &state.Runtime
	err = tx.QueryRowContext(ctx, "SELECT effective_mode,stage,current_phase_id,target_phase_id,generation,revision,active_batch_id::text,active_manual_intent_id::text,stage_deadline,last_evaluated_at,last_served,fault FROM junction_runtime WHERE junction_id=$1", id).Scan(&r.Mode, &r.Stage, &current, &target, &r.Generation, &r.Revision, &batchID, &manualID, &deadline, &r.LastEvaluatedAt, &lastServed, &r.Fault)
	if err != nil {
		return nil, err
	}
	r.CurrentPhase, r.TargetPhase, r.Deadline = current.String, target.String, timePointer(deadline)
	if err = json.Unmarshal(lastServed, &r.LastServed); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT direction,desired_state,actual_state,confirmed_command_id::text,confirmed_at FROM signal_states WHERE junction_id=$1", id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d domain.Direction
		var evidence domain.SignalEvidence
		var command sql.NullString
		var at sql.NullTime
		if err = rows.Scan(&d, &evidence.Desired, &evidence.Actual, &command, &at); err != nil {
			rows.Close()
			return nil, err
		}
		evidence.CommandID, evidence.ConfirmedAt = command.String, timePointer(at)
		state.Signals[d] = evidence
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT device_key,device_type,COALESCE(direction,''),status,last_reported_at FROM devices WHERE junction_id=$1", id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		var device domain.Device
		var at sql.NullTime
		if err = rows.Scan(&key, &device.Type, &device.Direction, &device.Status, &at); err != nil {
			rows.Close()
			return nil, err
		}
		device.ReportedAt = timePointer(at)
		state.Devices[key] = device
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT direction,vehicle_id,last_sequence_no,last_event_id,last_event_type FROM vehicle_trackers WHERE junction_id=$1", id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tracker domain.Tracker
		if err = rows.Scan(&tracker.Direction, &tracker.VehicleID, &tracker.Sequence, &tracker.EventID, &tracker.EventType); err != nil {
			rows.Close()
			return nil, err
		}
		state.Trackers[domain.TrackerKey(tracker.Direction, tracker.VehicleID)] = tracker
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT direction,vehicle_id,vehicle_type,arrival_event_id,accepted_at,emergency_expires_at,emergency_priority_active FROM queue_entries WHERE junction_id=$1", id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v domain.Vehicle
		var expiry sql.NullTime
		if err = rows.Scan(&v.Direction, &v.ID, &v.Type, &v.ArrivalEventID, &v.AcceptedAt, &expiry, &v.EmergencyActive); err != nil {
			rows.Close()
			return nil, err
		}
		v.EmergencyExpiresAt = timePointer(expiry)
		state.Vehicles[v.ID] = v
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if manualID.Valid {
		m := &domain.ManualIntent{ID: manualID.String}
		err = tx.QueryRowContext(ctx, "SELECT target_phase_id,requested_direction,accepted_at,expires_at FROM control_intents WHERE junction_id=$1 AND id=$2", id, m.ID).Scan(&m.Phase, &m.Direction, &m.AcceptedAt, &m.ExpiresAt)
		if err != nil {
			return nil, err
		}
		state.Manual = m
	}
	if batchID.Valid {
		b := &domain.Batch{ID: batchID.String}
		var phase sql.NullString
		var complete sql.NullTime
		err = tx.QueryRowContext(ctx, "SELECT generation,purpose,target_phase_id,status,issued_at,deadline_at,completed_at FROM command_batches WHERE junction_id=$1 AND id=$2", id, b.ID).Scan(&b.Generation, &b.Purpose, &phase, &b.Status, &b.IssuedAt, &b.Deadline, &complete)
		if err != nil {
			return nil, err
		}
		b.Phase, b.CompletedAt = phase.String, timePointer(complete)
		rows, err = tx.QueryContext(ctx, "SELECT id::text,direction,requested_state,status,acknowledged_at FROM controller_commands WHERE junction_id=$1 AND batch_id=$2 ORDER BY direction", id, b.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			cmd := domain.Command{BatchID: b.ID, JunctionID: id, Generation: b.Generation, IssuedAt: b.IssuedAt, ExpiresAt: b.Deadline}
			var at sql.NullTime
			if err = rows.Scan(&cmd.ID, &cmd.Direction, &cmd.Requested, &cmd.Status, &at); err != nil {
				rows.Close()
				return nil, err
			}
			cmd.AcknowledgedAt = timePointer(at)
			b.Commands = append(b.Commands, cmd)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		state.Pending = b
	}
	rows, err = tx.QueryContext(ctx, "SELECT code,message,opened_at FROM alerts WHERE junction_id=$1 AND resolved_at IS NULL", id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a domain.Alert
		if err = rows.Scan(&a.Code, &a.Message, &a.OpenedAt); err != nil {
			rows.Close()
			return nil, err
		}
		state.Alerts[a.Code] = a
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return state, nil
}

func timePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}

type unit struct {
	tx  *sql.Tx
	id  string
	now time.Time
}

func (u *unit) Now() time.Time { return u.now }

func (u *unit) ReserveEvent(ctx context.Context, kind, id, hash string, payload []byte) (*ports.EventRecord, error) {
	var query string
	switch kind {
	case "sensor":
		query = `INSERT INTO sensor_events(event_id,junction_id,claimed_junction_id,direction,vehicle_id,event_type,sequence_no,payload_hash,canonical_payload,outcome,sensor_at,received_at,accepted_at) VALUES($1,$2,$2,$4::jsonb->>'direction',$4::jsonb->>'vehicle_id',$4::jsonb->>'event_type',($4::jsonb->>'sequence_no')::bigint,$3,$4,'PROCESSING',($4::jsonb->>'timestamp')::timestamptz,$5,$5) ON CONFLICT(event_id) DO NOTHING`
	case "device":
		query = `INSERT INTO device_events(event_id,junction_id,claimed_junction_id,device_key,payload_hash,payload,outcome,device_at,received_at,accepted_at) VALUES($1,$2,$2,($4::jsonb->>'device_type') || ':' || COALESCE($4::jsonb->>'direction',''),$3,$4,'PROCESSING',($4::jsonb->>'timestamp')::timestamptz,$5,$5) ON CONFLICT(event_id) DO NOTHING`
	default:
		return nil, errors.New("unsupported event namespace")
	}
	result, err := u.tx.ExecContext(ctx, query, id, u.id, hash, string(payload), u.now)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, nil
	}
	table := "sensor_events"
	if kind == "device" {
		table = "device_events"
	}
	previous := &ports.EventRecord{}
	var body []byte
	err = u.tx.QueryRowContext(ctx, "SELECT payload_hash,result FROM "+table+" WHERE event_id=$1", id).Scan(&previous.Hash, &body)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(body, &previous.Result); err != nil {
		return nil, err
	}
	return previous, nil
}

func (u *unit) FinishEvent(ctx context.Context, kind, id string, result ports.Result) error {
	table := "sensor_events"
	if kind == "device" {
		table = "device_events"
	} else if kind != "sensor" {
		return errors.New("unsupported event namespace")
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = u.tx.ExecContext(ctx, "UPDATE "+table+" SET outcome=$2,result=$3 WHERE event_id=$1", id, result.Outcome, string(body))
	return err
}

func (u *unit) Command(ctx context.Context, id string) (domain.Command, error) {
	var command domain.Command
	var at sql.NullTime
	err := u.tx.QueryRowContext(ctx, `SELECT c.id::text,c.batch_id::text,c.junction_id,c.direction,c.requested_state,c.status,c.acknowledged_at,b.generation,b.issued_at,b.deadline_at FROM controller_commands c JOIN command_batches b ON b.id=c.batch_id WHERE c.id::text=$1 AND c.junction_id=$2`, id, u.id).Scan(&command.ID, &command.BatchID, &command.JunctionID, &command.Direction, &command.Requested, &command.Status, &at, &command.Generation, &command.IssuedAt, &command.ExpiresAt)
	command.AcknowledgedAt = timePointer(at)
	return command, translate(err)
}

func (u *unit) Feedback(ctx context.Context, feedback domain.Feedback, outcome string) error {
	body, err := json.Marshal(feedback)
	if err != nil {
		return err
	}
	_, err = u.tx.ExecContext(ctx, `INSERT INTO controller_feedback(command_id,claimed_command_id,junction_id,claimed_junction_id,status,actual_state,outcome,payload,controller_at,received_at) VALUES($1,$1,$2,$2,$3,$4,$5,$6,$7,$8)`, feedback.CommandID, u.id, feedback.Status, feedback.Actual, outcome, string(body), feedback.Timestamp, u.now)
	return err
}

func (u *unit) Save(ctx context.Context, s *domain.State, effects domain.Effects) error {
	if err := s.AssertSafe(); err != nil {
		return fmt.Errorf("refusing unsafe persistence: %w", err)
	}
	// Old pending batches are updated before new ones are inserted, preserving
	// the partial unique index even when one operation supersedes a batch.
	for _, b := range effects.Batches {
		target := map[domain.Direction]domain.Signal{}
		for _, c := range b.Commands {
			target[c.Direction] = c.Requested
		}
		body, err := json.Marshal(target)
		if err != nil {
			return err
		}
		_, err = u.tx.ExecContext(ctx, `INSERT INTO command_batches(id,junction_id,generation,purpose,target_phase_id,target_signals,status,issued_at,deadline_at,completed_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,completed_at=EXCLUDED.completed_at`, b.ID, u.id, b.Generation, b.Purpose, b.Phase, string(body), b.Status, b.IssuedAt, b.Deadline, b.CompletedAt)
		if err != nil {
			return err
		}
		for _, command := range b.Commands {
			_, err = u.tx.ExecContext(ctx, `INSERT INTO controller_commands(id,batch_id,junction_id,direction,requested_state,status,acknowledged_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,acknowledged_at=EXCLUDED.acknowledged_at`, command.ID, b.ID, u.id, command.Direction, command.Requested, command.Status, command.AcknowledgedAt)
			if err != nil {
				return err
			}
		}
	}
	manualID := ""
	if s.Manual != nil {
		manualID = s.Manual.ID
	}
	if _, err := u.tx.ExecContext(ctx, "UPDATE control_intents SET status='ENDED' WHERE junction_id=$1 AND status='ACTIVE' AND id::text<>$2", u.id, manualID); err != nil {
		return err
	}
	if m := s.Manual; m != nil {
		if _, err := u.tx.ExecContext(ctx, `INSERT INTO control_intents(id,junction_id,command,requested_direction,target_phase_id,status,accepted_at,expires_at) VALUES($1,$2,'MANUAL_GREEN_REQUEST',$3,$4,'ACTIVE',$5,$6) ON CONFLICT(id) DO NOTHING`, m.ID, u.id, m.Direction, m.Phase, m.AcceptedAt, m.ExpiresAt); err != nil {
			return err
		}
	}
	for _, d := range domain.Directions {
		v := s.Signals[d]
		if _, err := u.tx.ExecContext(ctx, `INSERT INTO signal_states(junction_id,direction,desired_state,actual_state,confirmed_command_id,confirmed_at) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6) ON CONFLICT(junction_id,direction) DO UPDATE SET desired_state=EXCLUDED.desired_state,actual_state=EXCLUDED.actual_state,confirmed_command_id=EXCLUDED.confirmed_command_id,confirmed_at=EXCLUDED.confirmed_at`, u.id, d, v.Desired, v.Actual, v.CommandID, v.ConfirmedAt); err != nil {
			return err
		}
	}
	for key, d := range s.Devices {
		if _, err := u.tx.ExecContext(ctx, `INSERT INTO devices(junction_id,device_key,device_type,direction,status,last_reported_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6) ON CONFLICT(junction_id,device_key) DO UPDATE SET status=EXCLUDED.status,last_reported_at=EXCLUDED.last_reported_at`, u.id, key, d.Type, d.Direction, d.Status, d.ReportedAt); err != nil {
			return err
		}
	}
	for _, tracker := range s.Trackers {
		if _, err := u.tx.ExecContext(ctx, `INSERT INTO vehicle_trackers(junction_id,direction,vehicle_id,last_sequence_no,last_event_id,last_event_type) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(junction_id,direction,vehicle_id) DO UPDATE SET last_sequence_no=EXCLUDED.last_sequence_no,last_event_id=EXCLUDED.last_event_id,last_event_type=EXCLUDED.last_event_type`, u.id, tracker.Direction, tracker.VehicleID, tracker.Sequence, tracker.EventID, tracker.EventType); err != nil {
			return err
		}
	}
	// Replacement of this junction's queue is atomic under its row lock; there
	// are no readers that can see the intermediate delete/insert projection.
	if _, err := u.tx.ExecContext(ctx, "DELETE FROM queue_entries WHERE junction_id=$1", u.id); err != nil {
		return err
	}
	for _, v := range s.Vehicles {
		if _, err := u.tx.ExecContext(ctx, `INSERT INTO queue_entries(junction_id,direction,vehicle_id,vehicle_type,arrival_event_id,accepted_at,emergency_expires_at,emergency_priority_active) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, u.id, v.Direction, v.ID, v.Type, v.ArrivalEventID, v.AcceptedAt, v.EmergencyExpiresAt, v.EmergencyActive); err != nil {
			return err
		}
	}
	codes := []string{}
	for code := range s.Alerts {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	if _, err := u.tx.ExecContext(ctx, "UPDATE alerts SET resolved_at=$2 WHERE junction_id=$1 AND resolved_at IS NULL AND NOT(code=ANY($3))", u.id, u.now, pq.Array(codes)); err != nil {
		return err
	}
	for _, a := range s.Alerts {
		if _, err := u.tx.ExecContext(ctx, `INSERT INTO alerts(junction_id,code,message,opened_at) VALUES($1,$2,$3,$4) ON CONFLICT(junction_id,code) WHERE resolved_at IS NULL DO NOTHING`, u.id, a.Code, a.Message, a.OpenedAt); err != nil {
			return err
		}
	}
	s.Runtime.Revision++
	r := s.Runtime
	lastServed, err := json.Marshal(r.LastServed)
	if err != nil {
		return err
	}
	batchID := ""
	if s.Pending != nil {
		batchID = s.Pending.ID
	}
	_, err = u.tx.ExecContext(ctx, `UPDATE junction_runtime SET effective_mode=$2,stage=$3,current_phase_id=NULLIF($4,''),target_phase_id=NULLIF($5,''),generation=$6,revision=$7,active_batch_id=NULLIF($8,'')::uuid,active_manual_intent_id=NULLIF($9,'')::uuid,stage_deadline=$10,last_evaluated_at=$11,last_served=$12,fault=$13,updated_at=$14 WHERE junction_id=$1`, u.id, r.Mode, r.Stage, r.CurrentPhase, r.TargetPhase, r.Generation, r.Revision, batchID, manualID, r.Deadline, r.LastEvaluatedAt, string(lastServed), r.Fault, u.now)
	if err != nil {
		return err
	}
	for _, a := range effects.Audit {
		if err = insertAudit(ctx, u.tx, u.id, r.Revision, a); err != nil {
			return err
		}
	}
	return nil
}

func insertAudit(ctx context.Context, tx *sql.Tx, id string, revision int64, a domain.Audit) error {
	if a.Details == nil {
		a.Details = map[string]any{}
	}
	body, err := json.Marshal(a.Details)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_log(junction_id,junction_revision,event_type,direction,command_id,details,occurred_at) VALUES(NULLIF($1,''),$2,$3,NULLIF($4,''),NULLIF($5,'')::uuid,$6,$7)`, id, revision, a.Type, a.Direction, a.CommandID, string(body), a.At)
	return err
}

func (s *Store) Reject(ctx context.Context, claimedID string, a domain.Audit) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id sql.NullString
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT j.id,r.revision FROM junctions j JOIN junction_runtime r ON r.junction_id=j.id WHERE j.id=$1", claimedID).Scan(&id, &revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if a.Details == nil {
		a.Details = map[string]any{}
	}
	a.Details["claimed_junction_id"] = claimedID
	if err = insertAudit(ctx, tx, id.String, revision, a); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) History(ctx context.Context, id string, before int64, limit int) ([]ports.HistoryItem, error) {
	var exists bool
	if err := s.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM junctions WHERE id=$1)", id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ports.ErrNotFound
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,junction_id,junction_revision,event_type,COALESCE(direction,''),COALESCE(command_id::text,''),details,occurred_at FROM audit_log WHERE junction_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3`, id, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ports.HistoryItem{}
	for rows.Next() {
		var item ports.HistoryItem
		if err = rows.Scan(&item.ID, &item.JunctionID, &item.Revision, &item.Type, &item.Direction, &item.CommandID, &item.Details, &item.At); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
