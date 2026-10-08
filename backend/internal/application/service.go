package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"factorytraffic/internal/domain"
	"factorytraffic/internal/ports"
)

type Service struct {
	Store  ports.Store
	Engine domain.Engine
	Log    *slog.Logger
}

func New(store ports.Store, log *slog.Logger) *Service {
	return &Service{Store: store, Engine: domain.Engine{NewID: NewID}, Log: log}
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("secure ID generation failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	x := hex.EncodeToString(b[:])
	return x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]
}

func FactoryConfig(id, name string) domain.Config {
	return domain.Config{ID: id, Name: name, Phases: []domain.Phase{{ID: "NORTH_SOUTH", Directions: []domain.Direction{domain.North, domain.South}}, {ID: "EAST_WEST", Directions: []domain.Direction{domain.East, domain.West}}}, Policy: domain.Policy{GreenSeconds: 30, YellowSeconds: 5, ClearanceSeconds: 2, ACKSeconds: 5, ManualSeconds: 120, EmergencySeconds: 120, StarvationSeconds: 120, AgeStepSeconds: 10, Weights: map[domain.VehicleType]int{domain.Truck: 3, domain.Forklift: 2, domain.Employee: 1, domain.EmergencyVehicle: 3}}}
}

func (s *Service) SeedAndRecover(ctx context.Context) error {
	if _, err := s.Store.Read(ctx, "A"); errors.Is(err, ports.ErrNotFound) {
		if err = s.Store.Create(ctx, FactoryConfig("A", "Junction A")); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	configs, err := s.Store.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range configs {
		err = s.Store.Transact(ctx, c.ID, func(state *domain.State, tx ports.Transaction) error {
			return tx.Save(ctx, state, s.Engine.Restart(state, tx.Now()))
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func canonical(value any) ([]byte, string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

func (s *Service) rejected(ctx context.Context, id, kind string, err error) {
	if auditErr := s.Store.Reject(ctx, id, domain.Audit{Type: kind, At: time.Now().UTC(), Details: map[string]any{"reason": err.Error()}}); auditErr != nil {
		s.Log.Error("could not persist rejection audit", "error", auditErr)
	}
}

func (s *Service) Sensor(ctx context.Context, event domain.SensorEvent) (ports.Result, error) {
	if err := event.Validate(); err != nil {
		s.rejected(ctx, event.JunctionID, "REJECTED_SENSOR_EVENT", err)
		return ports.Result{}, err
	}
	event.Timestamp = event.Timestamp.UTC()
	body, hash, err := canonical(event)
	if err != nil {
		return ports.Result{}, err
	}
	return s.event(ctx, "sensor", event.ID, event.JunctionID, hash, body, func(state *domain.State, now time.Time) (domain.Effects, error) {
		return s.Engine.Sensor(state, event, now)
	})
}

func (s *Service) Device(ctx context.Context, event domain.DeviceEvent) (ports.Result, error) {
	if err := event.Validate(); err != nil {
		s.rejected(ctx, event.JunctionID, "REJECTED_DEVICE_EVENT", err)
		return ports.Result{}, err
	}
	event.Timestamp = event.Timestamp.UTC()
	body, hash, err := canonical(event)
	if err != nil {
		return ports.Result{}, err
	}
	return s.event(ctx, "device", event.ID, event.JunctionID, hash, body, func(state *domain.State, now time.Time) (domain.Effects, error) {
		return s.Engine.Device(state, event, now)
	})
}

func (s *Service) event(ctx context.Context, kind, id, junction, hash string, body []byte, apply func(*domain.State, time.Time) (domain.Effects, error)) (ports.Result, error) {
	result := ports.Result{Outcome: "accepted", EventID: id}
	var businessErr error
	err := s.Store.Transact(ctx, junction, func(state *domain.State, tx ports.Transaction) error {
		previous, err := tx.ReserveEvent(ctx, kind, id, hash, body)
		if err != nil {
			return err
		}
		effects := domain.Effects{}
		if previous != nil {
			if previous.Hash != hash {
				businessErr = &domain.Error{Code: "conflict", Message: "event_id was already used for a different payload"}
				result.Outcome = "rejected"
				result.ErrorCode = "conflict"
				result.Message = businessErr.Error()
				effects.Record("CONFLICTING_EVENT_ID", tx.Now(), map[string]any{"event_id": id})
			} else {
				result = previous.Result
				if result.ErrorCode != "" {
					businessErr = &domain.Error{Code: result.ErrorCode, Message: result.Message}
				} else {
					result.Outcome = "duplicate"
				}
				effects.Record("DUPLICATE_EVENT", tx.Now(), map[string]any{"event_id": id, "namespace": kind})
			}
			return tx.Save(ctx, state, effects)
		}
		effects, businessErr = apply(state, tx.Now())
		if businessErr != nil {
			var typed *domain.Error
			if !errors.As(businessErr, &typed) {
				return businessErr
			}
			result.Outcome = "rejected"
			result.ErrorCode = typed.Code
			result.Message = typed.Message
			effects.Record("REJECTED_EVENT", tx.Now(), map[string]any{"event_id": id, "reason": typed.Message, "namespace": kind})
		}
		if err = tx.Save(ctx, state, effects); err != nil {
			return err
		}
		result.Revision = state.Runtime.Revision
		return tx.FinishEvent(ctx, kind, id, result)
	})
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			s.rejected(ctx, junction, "UNKNOWN_JUNCTION_EVENT", err)
		}
		return ports.Result{}, err
	}
	return result, businessErr
}

func (s *Service) Control(ctx context.Context, id string, request domain.ControlRequest) (ports.Result, error) {
	result := ports.Result{Outcome: "pending_physical_confirmation"}
	var businessErr error
	err := s.Store.Transact(ctx, id, func(state *domain.State, tx ports.Transaction) error {
		effects, err := s.Engine.Control(state, request, tx.Now())
		if err != nil {
			businessErr = err
			effects.Record("REJECTED_COMMAND", tx.Now(), map[string]any{"command": request.Command, "reason": err.Error()})
		}
		if err = tx.Save(ctx, state, effects); err != nil {
			return err
		}
		result.Revision = state.Runtime.Revision
		return nil
	})
	if err != nil {
		return ports.Result{}, err
	}
	return result, businessErr
}

func (s *Service) Feedback(ctx context.Context, feedback domain.Feedback) (ports.Result, error) {
	result := ports.Result{}
	var businessErr error
	err := s.Store.Transact(ctx, feedback.JunctionID, func(state *domain.State, tx ports.Transaction) error {
		known, err := tx.Command(ctx, feedback.CommandID)
		if err != nil {
			return err
		}
		effects, outcome, err := s.Engine.Acknowledge(state, known, feedback, tx.Now())
		if err != nil {
			businessErr = err
			effects.Record("REJECTED_CONTROLLER_EVENT", tx.Now(), map[string]any{"command_id": feedback.CommandID, "reason": err.Error()})
		}
		if err = tx.Feedback(ctx, feedback, outcome); err != nil {
			return err
		}
		if err = tx.Save(ctx, state, effects); err != nil {
			return err
		}
		result.Outcome = outcome
		result.Revision = state.Runtime.Revision
		return nil
	})
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			s.rejected(ctx, feedback.JunctionID, "UNKNOWN_CONTROLLER_COMMAND", err)
		}
		return ports.Result{}, err
	}
	return result, businessErr
}

func (s *Service) Tick(ctx context.Context, id string) error {
	return s.Store.Transact(ctx, id, func(state *domain.State, tx ports.Transaction) error {
		effects := s.Engine.Tick(state, tx.Now())
		if len(effects.Audit) == 0 && len(effects.Batches) == 0 {
			return nil
		}
		return tx.Save(ctx, state, effects)
	})
}

func (s *Service) RunScheduler(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			configs, err := s.Store.List(ctx)
			if err != nil {
				return err
			}
			for _, c := range configs {
				if err = s.Tick(ctx, c.ID); err != nil {
					return err
				}
			}
		}
	}
}

type Status struct {
	JunctionID       string                                     `json:"junction_id"`
	Name             string                                     `json:"name"`
	Revision         int64                                      `json:"revision"`
	Generation       int64                                      `json:"generation"`
	Mode             domain.Mode                                `json:"mode"`
	Stage            domain.Stage                               `json:"stage"`
	Phase            string                                     `json:"phase"`
	TargetPhase      string                                     `json:"target_phase"`
	ControllerStatus string                                     `json:"controller_status"`
	Desired          map[domain.Direction]domain.Signal         `json:"desired_signals"`
	Actual           map[domain.Direction]domain.Signal         `json:"actual_signals"`
	Evidence         map[domain.Direction]domain.SignalEvidence `json:"signal_evidence"`
	Queues           map[domain.Direction]int                   `json:"queues"`
	Vehicles         []domain.Vehicle                           `json:"vehicles"`
	Emergencies      []domain.Vehicle                           `json:"emergencies"`
	Devices          []domain.Device                            `json:"devices"`
	Alerts           []domain.Alert                             `json:"alerts"`
	Manual           *domain.ManualIntent                       `json:"manual"`
	Pending          *domain.Batch                              `json:"pending_batch"`
	Deadline         *time.Time                                 `json:"transition_deadline"`
	Sequence         map[domain.Direction]int64                 `json:"sequence_high_water"`
	Config           domain.Config                              `json:"config"`
}

func (s *Service) Status(ctx context.Context, id string) (Status, error) {
	state, err := s.Store.Read(ctx, id)
	if err != nil {
		return Status{}, err
	}
	return Snapshot(state), nil
}

func Snapshot(state *domain.State) Status {
	r := state.Runtime
	status := Status{JunctionID: state.Config.ID, Name: state.Config.Name, Revision: r.Revision, Generation: r.Generation, Mode: r.Mode, Stage: r.Stage, Phase: r.CurrentPhase, TargetPhase: r.TargetPhase, ControllerStatus: state.Devices[domain.DeviceKey("SIGNAL_CONTROLLER", "")].Status, Desired: map[domain.Direction]domain.Signal{}, Actual: map[domain.Direction]domain.Signal{}, Evidence: state.Signals, Queues: map[domain.Direction]int{}, Vehicles: []domain.Vehicle{}, Emergencies: []domain.Vehicle{}, Devices: []domain.Device{}, Alerts: []domain.Alert{}, Manual: state.Manual, Deadline: r.Deadline, Sequence: map[domain.Direction]int64{}, Config: state.Config}
	for _, d := range domain.Directions {
		status.Desired[d] = state.Signals[d].Desired
		status.Actual[d] = state.Signals[d].Actual
		status.Queues[d] = 0
		status.Sequence[d] = 0
	}
	for _, v := range state.Vehicles {
		status.Vehicles = append(status.Vehicles, v)
		status.Queues[v.Direction]++
		if v.EmergencyActive {
			status.Emergencies = append(status.Emergencies, v)
		}
	}
	for _, d := range state.Devices {
		status.Devices = append(status.Devices, d)
	}
	for _, a := range state.Alerts {
		status.Alerts = append(status.Alerts, a)
	}
	for _, t := range state.Trackers {
		if t.Sequence > status.Sequence[t.Direction] {
			status.Sequence[t.Direction] = t.Sequence
		}
	}
	sort.Slice(status.Vehicles, func(i, j int) bool { return status.Vehicles[i].ID < status.Vehicles[j].ID })
	sort.Slice(status.Emergencies, func(i, j int) bool {
		return status.Emergencies[i].ArrivalEventID < status.Emergencies[j].ArrivalEventID
	})
	sort.Slice(status.Devices, func(i, j int) bool {
		return domain.DeviceKey(status.Devices[i].Type, status.Devices[i].Direction) < domain.DeviceKey(status.Devices[j].Type, status.Devices[j].Direction)
	})
	sort.Slice(status.Alerts, func(i, j int) bool { return status.Alerts[i].Code < status.Alerts[j].Code })
	if state.Pending != nil && state.Pending.Status == "PENDING" {
		status.Pending = state.Pending
		status.Deadline = &state.Pending.Deadline
	}
	return status
}
