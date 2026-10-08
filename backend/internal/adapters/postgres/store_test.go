package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"factorytraffic/internal/domain"
	"factorytraffic/internal/ports"
)

func database(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for real PostgreSQL integration tests")
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func unique() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func configuration(id string) domain.Config {
	return domain.Config{ID: id, Name: "Integration test", Phases: []domain.Phase{{ID: "NS", Directions: []domain.Direction{domain.North, domain.South}}, {ID: "EW", Directions: []domain.Direction{domain.East, domain.West}}}, Policy: domain.Policy{GreenSeconds: 30, YellowSeconds: 5, ClearanceSeconds: 2, ACKSeconds: 5, ManualSeconds: 120, EmergencySeconds: 120, StarvationSeconds: 120, AgeStepSeconds: 10, Weights: map[domain.VehicleType]int{domain.Truck: 3, domain.Forklift: 2, domain.Employee: 1, domain.EmergencyVehicle: 3}}}
}

func TestPersistenceRollbackAndOwnership(t *testing.T) {
	s, ctx := database(t)
	id := "test-" + unique()
	if err := s.Create(ctx, configuration(id)); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Runtime.Stage != domain.Recovering || state.Signals[domain.North].Actual != domain.Unknown {
		t.Fatal("creation invented physical confirmation")
	}
	forced := errors.New("force rollback")
	err = s.Transact(ctx, id, func(state *domain.State, tx ports.Transaction) error {
		state.Runtime.Fault = "SHOULD_ROLL_BACK"
		if err := tx.Save(ctx, state, domain.Effects{}); err != nil {
			return err
		}
		return forced
	})
	if !errors.Is(err, forced) {
		t.Fatal(err)
	}
	reloaded, err := s.Read(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Runtime.Fault != "" || reloaded.Runtime.Revision != state.Runtime.Revision {
		t.Fatal("rollback retained a partial write")
	}
	owner, err := s.AcquireOwnership(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if second, err := s.AcquireOwnership(ctx); err == nil {
		second.Close()
		t.Fatal("two backend owners acquired controller coordination")
	}
}

func TestConcurrentDuplicateEventsAndDurableTombstone(t *testing.T) {
	s, ctx := database(t)
	id := "test-" + unique()
	if err := s.Create(ctx, configuration(id)); err != nil {
		t.Fatal(err)
	}
	event := domain.SensorEvent{ID: "event-" + unique(), JunctionID: id, Direction: domain.North, Type: "VEHICLE_ARRIVED", VehicleID: "v", VehicleType: domain.Truck, Sequence: 10, Timestamp: time.Now().UTC()}
	body, _ := json.Marshal(event)
	var wg sync.WaitGroup
	failures := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- s.Transact(ctx, id, func(state *domain.State, tx ports.Transaction) error {
				previous, err := tx.ReserveEvent(ctx, "sensor", event.ID, "same-hash", body)
				if err != nil {
					return err
				}
				if previous != nil {
					return nil
				}
				effects, err := (domain.Engine{}).Sensor(state, event, tx.Now())
				if err != nil {
					return err
				}
				if err = tx.Save(ctx, state, effects); err != nil {
					return err
				}
				return tx.FinishEvent(ctx, "sensor", event.ID, ports.Result{Outcome: "accepted", Revision: state.Runtime.Revision})
			})
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := s.Read(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Vehicles) != 1 {
		t.Fatal("duplicate arrivals changed queue more than once")
	}
	clear := event
	clear.ID = "clear-" + unique()
	clear.Type = "VEHICLE_CLEARED"
	clear.VehicleType = ""
	clear.Sequence = 11
	body, _ = json.Marshal(clear)
	err = s.Transact(ctx, id, func(state *domain.State, tx ports.Transaction) error {
		if _, err := tx.ReserveEvent(ctx, "sensor", clear.ID, "clear-hash", body); err != nil {
			return err
		}
		effects, err := (domain.Engine{}).Sensor(state, clear, tx.Now())
		if err != nil {
			return err
		}
		if err = tx.Save(ctx, state, effects); err != nil {
			return err
		}
		return tx.FinishEvent(ctx, "sensor", clear.ID, ports.Result{Outcome: "accepted", Revision: state.Runtime.Revision})
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.Read(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Vehicles) != 0 || state.Trackers[domain.TrackerKey(domain.North, "v")].Sequence != 11 {
		t.Fatal("clearance lost persisted ordering tombstone")
	}
	old := event
	old.ID = "old-" + unique()
	if _, err = (domain.Engine{}).Sensor(state, old, time.Now()); err == nil {
		t.Fatal("reload allowed old arrival to resurrect vehicle")
	}
}

func TestCrossJunctionEventIdentityAndCompositeConstraints(t *testing.T) {
	s, ctx := database(t)
	a, b := "test-"+unique(), "test-"+unique()
	for _, id := range []string{a, b} {
		if err := s.Create(ctx, configuration(id)); err != nil {
			t.Fatal(err)
		}
	}
	id := "event-" + unique()
	for index, junction := range []string{a, b} {
		event := domain.SensorEvent{ID: id, JunctionID: junction, Direction: domain.North, Type: "VEHICLE_CLEARED", VehicleID: "v", Sequence: 1, Timestamp: time.Now().UTC()}
		body, _ := json.Marshal(event)
		err := s.Transact(ctx, junction, func(state *domain.State, tx ports.Transaction) error {
			previous, err := tx.ReserveEvent(ctx, "sensor", id, junction, body)
			if err != nil {
				return err
			}
			if index == 1 {
				if previous == nil || previous.Hash != a {
					t.Fatal("event identity was scoped to a junction")
				}
				return nil
			}
			return tx.FinishEvent(ctx, "sensor", id, ports.Result{Outcome: "accepted"})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.DB.ExecContext(ctx, "INSERT INTO phase_members(junction_id,phase_id,direction) VALUES($1,'MISSING','NORTH')", a)
	if err == nil {
		t.Fatal("schema accepted a nonexistent phase")
	}
}
