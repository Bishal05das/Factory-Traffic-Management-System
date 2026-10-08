package domain

import "time"

type SensorEvent struct {
	ID          string      `json:"event_id"`
	JunctionID  string      `json:"junction_id"`
	Direction   Direction   `json:"direction"`
	Type        string      `json:"event_type"`
	VehicleID   string      `json:"vehicle_id"`
	VehicleType VehicleType `json:"vehicle_type,omitempty"`
	Sequence    int64       `json:"sequence_no"`
	Timestamp   time.Time   `json:"timestamp"`
}

func (event SensorEvent) Validate() error {
	if !ValidID(event.ID) || !ValidID(event.JunctionID) || !ValidID(event.VehicleID) {
		return invalid("event, junction and vehicle identifiers are required and must be valid")
	}
	if !ValidDirection(event.Direction) || event.Sequence <= 0 || event.Timestamp.IsZero() {
		return invalid("direction, positive sequence_no and timestamp are required")
	}
	if event.Type != "VEHICLE_ARRIVED" && event.Type != "VEHICLE_CLEARED" {
		return invalid("unsupported sensor event type")
	}
	if event.Type == "VEHICLE_ARRIVED" && !ValidVehicleType(event.VehicleType) {
		return invalid("arrival requires a supported vehicle_type")
	}
	if event.VehicleType != "" && !ValidVehicleType(event.VehicleType) {
		return invalid("unknown vehicle_type")
	}
	return nil
}

// Sensor assumes the application has checked event-id idempotency. Trackers
// remain after clearance, so delayed arrivals cannot resurrect cleared traffic.
func (e Engine) Sensor(s *State, event SensorEvent, now time.Time) (Effects, error) {
	out := Effects{}
	if err := event.Validate(); err != nil {
		return out, err
	}
	if event.JunctionID != s.Config.ID {
		return out, invalid("sensor event junction mismatch")
	}
	key := TrackerKey(event.Direction, event.VehicleID)
	if previous, exists := s.Trackers[key]; exists && event.Sequence <= previous.Sequence {
		return out, conflict("stale or reused vehicle sequence number")
	}
	vehicle, queued := s.Vehicles[event.VehicleID]
	if queued && vehicle.Direction != event.Direction {
		return out, conflict("vehicle is queued in another direction; clear it there first")
	}
	if queued && event.Type == "VEHICLE_ARRIVED" {
		return out, conflict("vehicle already queued")
	}
	now = s.Time(now)
	s.Trackers[key] = Tracker{VehicleID: event.VehicleID, Direction: event.Direction, Sequence: event.Sequence, EventID: event.ID, EventType: event.Type}
	if event.Type == "VEHICLE_ARRIVED" {
		vehicle = Vehicle{ID: event.VehicleID, Direction: event.Direction, Type: event.VehicleType, ArrivalEventID: event.ID, AcceptedAt: now}
		if event.VehicleType == EmergencyVehicle {
			expires := now.Add(seconds(s.Config.Policy.EmergencySeconds))
			vehicle.EmergencyActive, vehicle.EmergencyExpiresAt = true, &expires
			out.Record("EMERGENCY_DETECTED", now, map[string]any{"vehicle_id": event.VehicleID, "direction": event.Direction})
		}
		s.Vehicles[event.VehicleID] = vehicle
		out.Record("VEHICLE_DETECTED", now, map[string]any{"event_id": event.ID, "vehicle_id": event.VehicleID, "direction": event.Direction, "vehicle_type": event.VehicleType})
	} else {
		delete(s.Vehicles, event.VehicleID)
		kind := "VEHICLE_CLEARED"
		if !queued {
			kind = "UNMATCHED_CLEARANCE"
		}
		out.Record(kind, now, map[string]any{"event_id": event.ID, "vehicle_id": event.VehicleID, "direction": event.Direction})
		if queued && vehicle.EmergencyActive {
			out.Record("EMERGENCY_CLEARED", now, map[string]any{"vehicle_id": event.VehicleID})
		}
	}
	appendEffects(&out, e.Tick(s, now))
	return out, nil
}

type ControlRequest struct {
	Command   string    `json:"command"`
	Direction Direction `json:"direction,omitempty"`
}

func (e Engine) Control(s *State, request ControlRequest, now time.Time) (Effects, error) {
	out := Effects{}
	if request.Command == "RECOVER" {
		return e.Recover(s, now)
	}
	if request.Command != "MANUAL_GREEN_REQUEST" && request.Command != "RETURN_TO_AUTOMATIC" {
		return out, invalid("unsupported control command")
	}
	if request.Command == "MANUAL_GREEN_REQUEST" && !ValidDirection(request.Direction) {
		return out, invalid("manual request requires a supported direction")
	}
	if request.Command == "RETURN_TO_AUTOMATIC" && request.Direction != "" {
		return out, invalid("return to automatic does not take a direction")
	}
	if s.Runtime.RecoveryRequired || s.Runtime.Stage == FailureStop || s.Runtime.Stage == Recovering || !s.Healthy() {
		return out, conflict("junction must recover before accepting traffic control")
	}
	now = s.Time(now)
	if request.Command == "MANUAL_GREEN_REQUEST" {
		if s.Manual != nil {
			out.Record("MANUAL_REPLACED", now, map[string]any{"intent_id": s.Manual.ID})
		}
		s.Manual = &ManualIntent{ID: e.NewID(), Phase: s.Config.PhaseFor(request.Direction), Direction: request.Direction, AcceptedAt: now, ExpiresAt: now.Add(seconds(s.Config.Policy.ManualSeconds))}
		out.Record("MANUAL_OVERRIDE", now, map[string]any{"intent_id": s.Manual.ID, "direction": request.Direction, "expires_at": s.Manual.ExpiresAt})
	} else {
		s.Manual = nil
		out.Record("RETURN_TO_AUTOMATIC", now, nil)
	}
	appendEffects(&out, e.Tick(s, now))
	return out, nil
}

type DeviceEvent struct {
	ID         string    `json:"event_id"`
	JunctionID string    `json:"junction_id"`
	Type       string    `json:"device_type"`
	Direction  Direction `json:"direction,omitempty"`
	Status     string    `json:"status"`
	Timestamp  time.Time `json:"timestamp"`
}

func (event DeviceEvent) Validate() error {
	if !ValidID(event.ID) || !ValidID(event.JunctionID) || event.Timestamp.IsZero() {
		return invalid("device event requires event_id, junction_id and timestamp")
	}
	if event.Type != "SIGNAL_CONTROLLER" && event.Type != "SIGNAL" && event.Type != "SENSOR" {
		return invalid("unknown device_type")
	}
	if event.Type != "SIGNAL_CONTROLLER" && !ValidDirection(event.Direction) {
		return invalid("sensor and signal status require a direction")
	}
	if event.Type == "SIGNAL_CONTROLLER" && event.Direction != "" && !ValidDirection(event.Direction) {
		return invalid("invalid controller direction")
	}
	if event.Status != "ONLINE" && event.Status != "OFFLINE" && event.Status != "DEGRADED" && event.Status != "WARNING" && event.Status != "UNKNOWN" {
		return invalid("invalid device status")
	}
	return nil
}

func (e Engine) Device(s *State, event DeviceEvent, now time.Time) (Effects, error) {
	out := Effects{}
	if err := event.Validate(); err != nil {
		return out, err
	}
	if event.JunctionID != s.Config.ID {
		return out, invalid("device event junction mismatch")
	}
	now = s.Time(now)
	direction := event.Direction
	if event.Type == "SIGNAL_CONTROLLER" {
		direction = ""
	}
	s.Devices[DeviceKey(event.Type, direction)] = Device{Type: event.Type, Direction: direction, Status: event.Status, ReportedAt: &now}
	out.Record("DEVICE_STATUS", now, map[string]any{"device_type": event.Type, "direction": direction, "status": event.Status})
	if event.Status != "ONLINE" {
		e.Fail(s, "DEVICE_UNAVAILABLE", now, &out)
	} else {
		appendEffects(&out, e.Tick(s, now))
	}
	return out, nil
}

func appendEffects(out *Effects, extra Effects) {
	out.Batches = append(out.Batches, extra.Batches...)
	out.Audit = append(out.Audit, extra.Audit...)
}
