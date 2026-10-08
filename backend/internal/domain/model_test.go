package domain

import (
	"testing"
	"time"
)

func testConfig() Config {
	return Config{ID: "A", Name: "Junction A", Phases: []Phase{{ID: "NORTH_SOUTH", Directions: []Direction{North, South}}, {ID: "EAST_WEST", Directions: []Direction{East, West}}}, Policy: Policy{GreenSeconds: 30, YellowSeconds: 5, ClearanceSeconds: 2, ACKSeconds: 5, ManualSeconds: 120, EmergencySeconds: 120, StarvationSeconds: 120, AgeStepSeconds: 10, Weights: map[VehicleType]int{Truck: 3, Forklift: 2, Employee: 1, EmergencyVehicle: 3}}}
}

func TestConfigurationRejectsUnsafePhases(t *testing.T) {
	c := testConfig()
	c.Phases[0].Directions = []Direction{North, East}
	c.Phases[1].Directions = []Direction{South, West}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted conflicting movements in one phase")
	}
}

func TestConfigurationRequiresExplicitDurations(t *testing.T) {
	c := testConfig()
	c.Policy.ClearanceSeconds = 0
	if err := c.Validate(); err == nil {
		t.Fatal("silently accepted a missing clearance interval")
	}
}

func TestInitialSignalsAreUnconfirmed(t *testing.T) {
	s, err := NewState(testConfig(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Runtime.Stage != Recovering || s.Healthy() {
		t.Fatal("new junction must need physical reconciliation")
	}
	for _, d := range Directions {
		if s.Signals[d].Desired != Red || s.Signals[d].Actual != Unknown {
			t.Fatalf("unexpected initial evidence for %s", d)
		}
	}
}

func TestClockCannotMoveBackward(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	s, _ := NewState(testConfig(), now)
	if got := s.Time(now.Add(-time.Hour)); !got.Equal(now) {
		t.Fatal("backward time shortened a persisted control interval")
	}
}

func TestExpiredEmergencyWeightMustMatchTruckPolicy(t *testing.T) {
	c := testConfig()
	c.Policy.Weights[EmergencyVehicle] = 1
	if err := c.Validate(); err == nil {
		t.Fatal("configuration contradicted expired-emergency truck-weight policy")
	}
}
