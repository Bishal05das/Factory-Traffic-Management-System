package domain

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

type harness struct {
	e        Engine
	s        *State
	now      time.Time
	ids      int
	sequence int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{now: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	h.e = Engine{NewID: func() string { h.ids++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", h.ids) }}
	var err error
	h.s, err = NewState(testConfig(), h.now)
	if err != nil {
		t.Fatal(err)
	}
	for key, d := range h.s.Devices {
		d.Status = "ONLINE"
		h.s.Devices[key] = d
	}
	h.e.Tick(h.s, h.now)
	h.ackAll(t)
	return h
}

func (h *harness) ackAll(t *testing.T) {
	t.Helper()
	commands := append([]Command(nil), h.s.Pending.Commands...)
	for _, command := range commands {
		if command.Status != "PENDING" {
			continue
		}
		_, _, err := h.e.Acknowledge(h.s, command, Feedback{CommandID: command.ID, JunctionID: "A", Status: "ACK", Actual: command.Requested}, h.now)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.AssertSafe(); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) arrival(t *testing.T, id string, direction Direction, kind VehicleType) {
	t.Helper()
	h.sequence++
	_, err := h.e.Sensor(h.s, SensorEvent{ID: fmt.Sprintf("event-%d", h.sequence), JunctionID: "A", Direction: direction, Type: "VEHICLE_ARRIVED", VehicleID: id, VehicleType: kind, Sequence: h.sequence, Timestamp: h.now}, h.now)
	if err != nil {
		t.Fatal(err)
	}
}

func (h *harness) tick(d time.Duration) { h.now = h.now.Add(d); h.e.Tick(h.s, h.now) }

func northGreen(t *testing.T) *harness {
	h := newHarness(t)
	h.arrival(t, "north-truck", North, Truck)
	h.tick(2 * time.Second)
	h.ackAll(t)
	if h.s.Runtime.Stage != SteadyGreen || h.s.Runtime.CurrentPhase != "NORTH_SOUTH" {
		t.Fatal("north phase was not granted")
	}
	return h
}

func TestClearanceNeedsAllAcknowledgementsAndFullHold(t *testing.T) {
	h := newHarness(t)
	h.arrival(t, "east", East, Truck)
	h.tick(time.Second)
	if h.s.Runtime.Stage != AllRedHold {
		t.Fatal("clearance was abbreviated")
	}
	h.tick(time.Second)
	if h.s.Runtime.Stage != WaitGreen {
		t.Fatal("eligible green was not requested")
	}
	command := h.s.Pending.Commands[0]
	h.e.Acknowledge(h.s, command, Feedback{CommandID: command.ID, JunctionID: "A", Status: "ACK", Actual: command.Requested}, h.now)
	if h.s.Runtime.Stage != WaitGreen || h.s.Runtime.Deadline != nil {
		t.Fatal("partial ACK started green timer")
	}
	h.ackAll(t)
	if h.s.Runtime.Stage != SteadyGreen {
		t.Fatal("complete ACK did not establish green")
	}
}

func TestEmergencyPreemptionUsesConfirmedYellowAndRed(t *testing.T) {
	h := northGreen(t)
	h.arrival(t, "ambulance", East, EmergencyVehicle)
	if h.s.Runtime.Stage != WaitYellow || h.s.Signals[East].Desired != Red {
		t.Fatal("emergency bypassed yellow")
	}
	h.tick(time.Second)
	h.ackAll(t)
	h.tick(4 * time.Second)
	if h.s.Runtime.Stage != YellowHold {
		t.Fatal("yellow timer started before complete ACK")
	}
	h.tick(time.Second)
	if h.s.Runtime.Stage != WaitRed {
		t.Fatal("missing all-red transition")
	}
	h.ackAll(t)
	h.tick(time.Second)
	if h.s.Runtime.Stage != AllRedHold {
		t.Fatal("clearance shortened")
	}
	h.tick(time.Second)
	h.ackAll(t)
	if h.s.Runtime.CurrentPhase != "EAST_WEST" || h.s.Runtime.Mode != Emergency {
		t.Fatal("emergency phase not established")
	}
	if _, exists := h.s.Vehicles["ambulance"]; !exists {
		t.Fatal("green feedback cleared a vehicle")
	}
}

func TestEmergencyDuringPendingGreenKeepsBatchPhase(t *testing.T) {
	h := newHarness(t)
	h.arrival(t, "north", North, Truck)
	h.tick(2 * time.Second)
	if h.s.Runtime.Stage != WaitGreen {
		t.Fatal("expected pending north green")
	}
	h.arrival(t, "emergency", East, EmergencyVehicle)
	if h.s.Pending.Phase != "NORTH_SOUTH" {
		t.Fatal("intent overwrote executing batch")
	}
	h.ackAll(t)
	if h.s.Runtime.CurrentPhase != "NORTH_SOUTH" || h.s.Runtime.Stage != WaitYellow {
		t.Fatal("pending green was attributed to wrong phase or was not preempted")
	}
	if h.s.Signals[East].Desired != Red {
		t.Fatal("conflicting green issued")
	}
}

func TestTimeoutDoesNotAssumePhysicalExecutionOrRetryForever(t *testing.T) {
	h := newHarness(t)
	h.arrival(t, "east", East, Truck)
	h.tick(2 * time.Second)
	h.tick(5 * time.Second)
	if h.s.Runtime.Stage != FailureStop {
		t.Fatal("missing timeout failure")
	}
	for _, signal := range h.s.Signals {
		if signal.Actual != Unknown || signal.Desired != Red {
			t.Fatal("timeout trusted unconfirmed state")
		}
	}
	generation := h.s.Runtime.Generation
	h.tick(5 * time.Second)
	h.tick(time.Hour)
	if h.s.Runtime.Generation != generation {
		t.Fatal("fault generated repeated stop batches")
	}
	if _, err := h.e.Control(h.s, ControlRequest{Command: "MANUAL_GREEN_REQUEST", Direction: West}, h.now); err == nil {
		t.Fatal("manual operation bypassed failure")
	}
}

func TestMismatchFailsSafely(t *testing.T) {
	h := newHarness(t)
	h.arrival(t, "east", East, Truck)
	h.tick(2 * time.Second)
	cmd := h.s.Pending.Commands[0]
	_, outcome, err := h.e.Acknowledge(h.s, cmd, Feedback{CommandID: cmd.ID, JunctionID: "A", Status: "ACK", Actual: Green}, h.now)
	if err != nil || outcome != "fault" || h.s.Runtime.Stage != FailureStop {
		t.Fatal("mismatched actual state was accepted")
	}
}

func TestManualEmergencyAndExpiry(t *testing.T) {
	h := northGreen(t)
	if _, err := h.e.Control(h.s, ControlRequest{Command: "MANUAL_GREEN_REQUEST", Direction: North}, h.now); err != nil {
		t.Fatal(err)
	}
	h.arrival(t, "emergency", East, EmergencyVehicle)
	if h.s.Runtime.Mode != Emergency || h.s.Manual == nil {
		t.Fatal("emergency did not temporarily override manual")
	}
	h.sequence++
	_, err := h.e.Sensor(h.s, SensorEvent{ID: "clear-emergency", JunctionID: "A", Direction: East, Type: "VEHICLE_CLEARED", VehicleID: "emergency", Sequence: h.sequence, Timestamp: h.now}, h.now)
	if err != nil {
		t.Fatal(err)
	}
	if h.s.Runtime.Mode != Manual || h.s.Runtime.Stage != WaitYellow {
		t.Fatal("manual not restored or yellow was canceled")
	}
	h.ackAll(t)
	h.tick(5 * time.Second)
	h.ackAll(t)
	h.tick(2 * time.Second)
	h.ackAll(t)
	h.tick(120 * time.Second)
	if h.s.Manual != nil || h.s.Runtime.Mode != Automatic {
		t.Fatal("manual expiry not applied")
	}
}

func TestClearanceTombstoneRejectsOlderArrival(t *testing.T) {
	h := newHarness(t)
	clear := SensorEvent{ID: "clear", JunctionID: "A", Direction: North, Type: "VEHICLE_CLEARED", VehicleID: "v", Sequence: 20, Timestamp: h.now}
	if _, err := h.e.Sensor(h.s, clear, h.now); err != nil {
		t.Fatal(err)
	}
	arrival := clear
	arrival.ID = "arrival"
	arrival.Type = "VEHICLE_ARRIVED"
	arrival.VehicleType = Truck
	arrival.Sequence = 19
	if _, err := h.e.Sensor(h.s, arrival, h.now); err == nil {
		t.Fatal("delayed arrival resurrected cleared vehicle")
	}
	arrival.VehicleID = "other"
	if _, err := h.e.Sensor(h.s, arrival, h.now); err != nil {
		t.Fatal("direction-wide sequence rejected a different vehicle")
	}
	if len(h.s.Vehicles) != 1 {
		t.Fatal("incorrect queue")
	}
}

func TestCompetingEmergencyFirstAcceptedWinsAndExpires(t *testing.T) {
	h := newHarness(t)
	h.arrival(t, "first", North, EmergencyVehicle)
	h.now = h.now.Add(time.Second)
	h.arrival(t, "second", East, EmergencyVehicle)
	phase, mode := h.s.Select(h.now)
	if phase != "NORTH_SOUTH" || mode != Emergency {
		t.Fatal("competing emergency order changed")
	}
	h.tick(120 * time.Second)
	if h.s.Vehicles["first"].EmergencyActive || h.s.Vehicles["second"].EmergencyActive || len(h.s.Vehicles) != 2 {
		t.Fatal("expiry must remove priority without removing queue data")
	}
}

func TestStarvationAccountsForLastService(t *testing.T) {
	h := northGreen(t)
	h.arrival(t, "employee", East, Employee)
	// The north queue can remain uncleared, but its confirmed service resets its
	// scheduling wait; it must not block east indefinitely as the oldest arrival.
	h.s.Runtime.LastServed["NORTH_SOUTH"] = h.now.Add(100 * time.Second)
	phase, _ := h.s.Select(h.now.Add(120 * time.Second))
	if phase != "EAST_WEST" {
		t.Fatal("uncleared current queue defeated starvation protection")
	}
}

func TestRestartInvalidatesPhysicalStateAtEveryStage(t *testing.T) {
	for _, stage := range []Stage{WaitGreen, SteadyGreen, WaitYellow, YellowHold, WaitRed, AllRedHold} {
		t.Run(string(stage), func(t *testing.T) {
			h := northGreen(t)
			h.s.Runtime.Stage = stage
			generation := h.s.Runtime.Generation
			h.e.Restart(h.s, h.now)
			if h.s.Runtime.Stage != Recovering || h.s.Runtime.Generation <= generation || h.s.Runtime.Deadline != nil || h.s.Healthy() {
				t.Fatal("restart failed to invalidate control continuity")
			}
			if len(h.s.Vehicles) != 1 {
				t.Fatal("restart lost vehicles")
			}
			for _, signal := range h.s.Signals {
				if signal.Actual != Unknown || signal.Desired != Red {
					t.Fatal("restart reused stale physical evidence")
				}
			}
		})
	}
}

func TestSensorOfflineRequiresExplicitRecovery(t *testing.T) {
	h := northGreen(t)
	event := DeviceEvent{ID: "offline", JunctionID: "A", Type: "SENSOR", Direction: South, Status: "OFFLINE", Timestamp: h.now}
	h.e.Device(h.s, event, h.now)
	if h.s.Runtime.Stage != FailureStop {
		t.Fatal("sensor fault did not stop grants")
	}
	event.ID, event.Status = "online", "ONLINE"
	h.e.Device(h.s, event, h.now)
	if h.s.Runtime.Stage != FailureStop {
		t.Fatal("ONLINE bypassed recovery")
	}
	if _, err := h.e.Recover(h.s, h.now); err != nil {
		t.Fatal(err)
	}
	if h.s.Runtime.Stage != WaitRed || !h.s.AllActual(Unknown) && h.s.Signals[North].Actual != Unknown {
		t.Fatal("recovery reused old evidence")
	}
}

func TestGeneratedOperationsPreserveSignalSafety(t *testing.T) {
	h := newHarness(t)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		h.now = h.now.Add(time.Duration(rng.Intn(3)) * time.Second)
		switch rng.Intn(5) {
		case 0:
			h.arrival(t, fmt.Sprintf("vehicle-%d", i), Directions[rng.Intn(4)], []VehicleType{Truck, Forklift, Employee, EmergencyVehicle}[rng.Intn(4)])
		case 1:
			if h.s.Pending != nil && h.s.Pending.Status == "PENDING" {
				h.ackAll(t)
			}
		case 2:
			h.e.Control(h.s, ControlRequest{Command: "MANUAL_GREEN_REQUEST", Direction: Directions[rng.Intn(4)]}, h.now)
		case 3:
			h.e.Control(h.s, ControlRequest{Command: "RETURN_TO_AUTOMATIC"}, h.now)
		case 4:
			if h.s.Runtime.Stage == FailureStop {
				h.e.Recover(h.s, h.now)
			}
		}
		h.e.Tick(h.s, h.now)
		if err := h.s.AssertSafe(); err != nil {
			t.Fatalf("operation %d: %v", i, err)
		}
	}
}
