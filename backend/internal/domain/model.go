// Package domain implements traffic rules without HTTP or database dependencies.
package domain

import (
	"fmt"
	"strings"
	"time"
)

type Direction string
type Signal string
type Mode string
type Stage string
type VehicleType string

const (
	North Direction = "NORTH"
	South Direction = "SOUTH"
	East  Direction = "EAST"
	West  Direction = "WEST"

	Red     Signal = "RED"
	Yellow  Signal = "YELLOW"
	Green   Signal = "GREEN"
	Unknown Signal = "UNKNOWN"

	Automatic Mode = "AUTOMATIC"
	Manual    Mode = "MANUAL"
	Emergency Mode = "EMERGENCY"
	Failure   Mode = "FAILURE"

	Recovering  Stage = "RECOVERING"
	WaitRed     Stage = "WAIT_RED"
	AllRedHold  Stage = "ALL_RED_HOLD"
	WaitGreen   Stage = "WAIT_GREEN"
	SteadyGreen Stage = "GREEN"
	WaitYellow  Stage = "WAIT_YELLOW"
	YellowHold  Stage = "YELLOW_HOLD"
	FailureStop Stage = "FAILURE_STOP"

	Forklift         VehicleType = "FORKLIFT"
	Truck            VehicleType = "TRUCK"
	Employee         VehicleType = "EMPLOYEE_VEHICLE"
	EmergencyVehicle VehicleType = "EMERGENCY"
)

var Directions = []Direction{North, South, East, West}

type Phase struct {
	ID         string      `json:"id"`
	Directions []Direction `json:"directions"`
}

// Policy has no implicit operating defaults. The application must supply a
// validated configuration; tests may provide their own durations and weights.
type Policy struct {
	GreenSeconds      int                 `json:"green_seconds"`
	YellowSeconds     int                 `json:"yellow_seconds"`
	ClearanceSeconds  int                 `json:"clearance_seconds"`
	ACKSeconds        int                 `json:"ack_seconds"`
	ManualSeconds     int                 `json:"manual_seconds"`
	EmergencySeconds  int                 `json:"emergency_seconds"`
	StarvationSeconds int                 `json:"starvation_seconds"`
	AgeStepSeconds    int                 `json:"age_step_seconds"`
	Weights           map[VehicleType]int `json:"weights"`
}

type Config struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Phases []Phase `json:"phases"`
	Policy Policy  `json:"policy"`
}

type SignalEvidence struct {
	Desired     Signal     `json:"desired"`
	Actual      Signal     `json:"actual"`
	CommandID   string     `json:"command_id,omitempty"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
}

type Runtime struct {
	Mode             Mode                 `json:"mode"`
	Stage            Stage                `json:"stage"`
	CurrentPhase     string               `json:"current_phase,omitempty"`
	TargetPhase      string               `json:"target_phase,omitempty"`
	Generation       int64                `json:"generation"`
	Revision         int64                `json:"revision"`
	Deadline         *time.Time           `json:"deadline,omitempty"`
	LastEvaluatedAt  time.Time            `json:"last_evaluated_at"`
	Fault            string               `json:"fault,omitempty"`
	LastServed       map[string]time.Time `json:"last_served"`
	RecoveryRequired bool                 `json:"recovery_required"`
}

type Command struct {
	ID             string     `json:"command_id"`
	BatchID        string     `json:"batch_id"`
	JunctionID     string     `json:"junction_id"`
	Generation     int64      `json:"generation"`
	Direction      Direction  `json:"direction"`
	Requested      Signal     `json:"requested_state"`
	Status         string     `json:"status"`
	IssuedAt       time.Time  `json:"issued_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

type Batch struct {
	ID          string     `json:"batch_id"`
	Generation  int64      `json:"generation"`
	Purpose     Stage      `json:"purpose"`
	Phase       string     `json:"target_phase,omitempty"`
	Status      string     `json:"status"`
	Commands    []Command  `json:"commands"`
	IssuedAt    time.Time  `json:"issued_at"`
	Deadline    time.Time  `json:"deadline"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type Vehicle struct {
	ID                 string      `json:"vehicle_id"`
	Direction          Direction   `json:"direction"`
	Type               VehicleType `json:"vehicle_type"`
	ArrivalEventID     string      `json:"arrival_event_id"`
	AcceptedAt         time.Time   `json:"accepted_at"`
	EmergencyExpiresAt *time.Time  `json:"emergency_expires_at,omitempty"`
	EmergencyActive    bool        `json:"emergency_active"`
}

type Tracker struct {
	VehicleID string    `json:"vehicle_id"`
	Direction Direction `json:"direction"`
	Sequence  int64     `json:"sequence_no"`
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"`
}

type ManualIntent struct {
	ID         string    `json:"id"`
	Phase      string    `json:"phase"`
	Direction  Direction `json:"direction"`
	AcceptedAt time.Time `json:"accepted_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type Device struct {
	Type       string     `json:"device_type"`
	Direction  Direction  `json:"direction,omitempty"`
	Status     string     `json:"status"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
}

type Alert struct {
	Code     string    `json:"code"`
	Message  string    `json:"message"`
	OpenedAt time.Time `json:"opened_at"`
}

type State struct {
	Config   Config                       `json:"config"`
	Runtime  Runtime                      `json:"runtime"`
	Signals  map[Direction]SignalEvidence `json:"signals"`
	Devices  map[string]Device            `json:"devices"`
	Vehicles map[string]Vehicle           `json:"vehicles"`
	Trackers map[string]Tracker           `json:"trackers"`
	Manual   *ManualIntent                `json:"manual,omitempty"`
	Pending  *Batch                       `json:"pending,omitempty"`
	Alerts   map[string]Alert             `json:"alerts"`
}

type Audit struct {
	Type      string         `json:"event_type"`
	At        time.Time      `json:"timestamp"`
	Direction Direction      `json:"direction,omitempty"`
	CommandID string         `json:"command_id,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

type Effects struct {
	Batches []Batch
	Audit   []Audit
}

func (e *Effects) Record(kind string, at time.Time, details map[string]any) {
	e.Audit = append(e.Audit, Audit{Type: kind, At: at, Details: details})
}

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string      { return e.Message }
func invalid(message string) error  { return &Error{Code: "invalid", Message: message} }
func conflict(message string) error { return &Error{Code: "conflict", Message: message} }

func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}

func ValidDirection(d Direction) bool {
	return d == North || d == South || d == East || d == West
}

func ValidVehicleType(v VehicleType) bool {
	return v == Truck || v == Forklift || v == Employee || v == EmergencyVehicle
}

func (c Config) Validate() error {
	if !ValidID(c.ID) || strings.TrimSpace(c.Name) == "" || len(c.Name) > 200 {
		return invalid("junction requires a valid id and name")
	}
	if len(c.Phases) != 2 {
		return invalid("exactly two compatible traffic phases are required")
	}
	seenPhases, seenDirections := map[string]bool{}, map[Direction]bool{}
	for _, p := range c.Phases {
		if !ValidID(p.ID) || seenPhases[p.ID] || len(p.Directions) != 2 {
			return invalid("phases require unique ids and two directions each")
		}
		seenPhases[p.ID] = true
		for _, d := range p.Directions {
			if !ValidDirection(d) || seenDirections[d] {
				return invalid("each supported direction must occur in exactly one phase")
			}
			seenDirections[d] = true
		}
		if !((contains(p.Directions, North) && contains(p.Directions, South)) || (contains(p.Directions, East) && contains(p.Directions, West))) {
			return invalid("only north/south and east/west movements are compatible")
		}
	}
	values := []int{c.Policy.GreenSeconds, c.Policy.YellowSeconds, c.Policy.ClearanceSeconds, c.Policy.ACKSeconds, c.Policy.ManualSeconds, c.Policy.EmergencySeconds, c.Policy.StarvationSeconds, c.Policy.AgeStepSeconds}
	for _, v := range values {
		if v <= 0 || v > 86400 {
			return invalid("policy durations must be explicitly set between 1 and 86400 seconds")
		}
	}
	if len(c.Policy.Weights) != 4 {
		return invalid("normal scheduling weights must be supplied for all vehicle types")
	}
	for _, v := range []VehicleType{Truck, Forklift, Employee, EmergencyVehicle} {
		if c.Policy.Weights[v] <= 0 || c.Policy.Weights[v] > 1000 {
			return invalid(fmt.Sprintf("invalid scheduling weight for %s", v))
		}
	}
	if !(c.Policy.Weights[Truck] > c.Policy.Weights[Forklift] && c.Policy.Weights[Forklift] > c.Policy.Weights[Employee]) {
		return invalid("weights must preserve truck > forklift > employee priority")
	}
	return nil
}

func contains(ds []Direction, d Direction) bool {
	for _, candidate := range ds {
		if candidate == d {
			return true
		}
	}
	return false
}

func (c Config) Phase(id string) (Phase, bool) {
	for _, p := range c.Phases {
		if p.ID == id {
			return p, true
		}
	}
	return Phase{}, false
}

func (c Config) PhaseFor(d Direction) string {
	for _, p := range c.Phases {
		if contains(p.Directions, d) {
			return p.ID
		}
	}
	return ""
}

func NewState(c Config, now time.Time) (*State, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	s := &State{Config: c, Runtime: Runtime{Mode: Failure, Stage: Recovering, RecoveryRequired: true, LastEvaluatedAt: now.UTC(), LastServed: map[string]time.Time{}}, Signals: map[Direction]SignalEvidence{}, Devices: map[string]Device{}, Vehicles: map[string]Vehicle{}, Trackers: map[string]Tracker{}, Alerts: map[string]Alert{}}
	for _, d := range Directions {
		s.Signals[d] = SignalEvidence{Desired: Red, Actual: Unknown}
		for _, kind := range []string{"SENSOR", "SIGNAL"} {
			s.Devices[DeviceKey(kind, d)] = Device{Type: kind, Direction: d, Status: "UNKNOWN"}
		}
	}
	s.Devices[DeviceKey("SIGNAL_CONTROLLER", "")] = Device{Type: "SIGNAL_CONTROLLER", Status: "UNKNOWN"}
	return s, nil
}

func DeviceKey(kind string, d Direction) string       { return kind + ":" + string(d) }
func TrackerKey(d Direction, vehicleID string) string { return string(d) + ":" + vehicleID }

func (s *State) Healthy() bool {
	for _, device := range s.Devices {
		if device.Status != "ONLINE" {
			return false
		}
	}
	return len(s.Devices) == 9
}

func (s *State) Time(now time.Time) time.Time {
	now = now.UTC()
	if now.Before(s.Runtime.LastEvaluatedAt) {
		return s.Runtime.LastEvaluatedAt
	}
	s.Runtime.LastEvaluatedAt = now
	return now
}

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }
