package domain

import (
	"fmt"
	"time"
)

// Engine receives an ID factory so transitions are reproducible in tests.
// A loaded State belongs to one transaction; no caller shares it concurrently.
type Engine struct{ NewID func() string }

func (e Engine) request(s *State, purpose Stage, phase string, color Signal, now time.Time, out *Effects) {
	if s.Pending != nil && s.Pending.Status == "PENDING" {
		s.Pending.Status = "SUPERSEDED"
		for i := range s.Pending.Commands {
			if s.Pending.Commands[i].Status == "PENDING" {
				s.Pending.Commands[i].Status = "SUPERSEDED"
			}
		}
		out.Batches = append(out.Batches, *s.Pending)
	}
	s.Runtime.Generation++
	b := Batch{ID: e.NewID(), Generation: s.Runtime.Generation, Purpose: purpose, Phase: phase, Status: "PENDING", IssuedAt: now, Deadline: now.Add(seconds(s.Config.Policy.ACKSeconds))}
	for _, d := range Directions {
		requested := Red
		if phase != "" && s.Config.PhaseFor(d) == phase {
			requested = color
		}
		cmd := Command{ID: e.NewID(), BatchID: b.ID, JunctionID: s.Config.ID, Generation: b.Generation, Direction: d, Requested: requested, Status: "PENDING", IssuedAt: now, ExpiresAt: b.Deadline}
		b.Commands = append(b.Commands, cmd)
		evidence := s.Signals[d]
		evidence.Desired = requested
		s.Signals[d] = evidence
		out.Audit = append(out.Audit, Audit{Type: "SIGNAL_STATE_REQUESTED", At: now, Direction: d, CommandID: cmd.ID, Details: map[string]any{"requested_state": requested, "batch_id": b.ID, "generation": b.Generation}})
	}
	s.Pending = &b
	s.Runtime.Stage = purpose
	s.Runtime.Deadline = nil
	out.Batches = append(out.Batches, b)
	out.Record("TRANSITION_STARTED", now, map[string]any{"stage": purpose, "batch_id": b.ID, "target_phase": phase})
}

func (e Engine) Fail(s *State, reason string, now time.Time, out *Effects) {
	now = s.Time(now)
	alreadyFailed := s.Runtime.Stage == FailureStop
	s.Runtime.Fault = reason
	s.Runtime.RecoveryRequired = true
	s.Runtime.Mode = Failure
	s.Runtime.TargetPhase = ""
	s.Runtime.Deadline = nil
	for _, d := range Directions {
		s.Signals[d] = SignalEvidence{Desired: Red, Actual: Unknown}
	}
	if _, exists := s.Alerts[reason]; !exists {
		s.Alerts[reason] = Alert{Code: reason, Message: reason, OpenedAt: now}
	}
	if !alreadyFailed {
		out.Record("FAILURE_ENTERED", now, map[string]any{"reason": reason})
		controller := s.Devices[DeviceKey("SIGNAL_CONTROLLER", "")]
		if controller.Status == "ONLINE" {
			e.request(s, FailureStop, "", Red, now, out)
		} else {
			if s.Pending != nil && s.Pending.Status == "PENDING" {
				s.Pending.Status = "SUPERSEDED"
				for i := range s.Pending.Commands {
					if s.Pending.Commands[i].Status == "PENDING" {
						s.Pending.Commands[i].Status = "SUPERSEDED"
					}
				}
				out.Batches = append(out.Batches, *s.Pending)
			}
			s.Runtime.Stage = FailureStop
		}
	}
}

func (e Engine) Recover(s *State, now time.Time) (Effects, error) {
	out := Effects{}
	if s.Runtime.Stage != FailureStop && s.Runtime.Stage != Recovering {
		return out, conflict("recovery is only allowed after a fault or during startup reconciliation")
	}
	if !s.Healthy() {
		return out, conflict("all configured devices must report ONLINE before recovery")
	}
	now = s.Time(now)
	s.Runtime.Mode = Failure
	s.Runtime.RecoveryRequired = true
	s.Runtime.Fault = ""
	s.Runtime.CurrentPhase = ""
	s.Runtime.TargetPhase = ""
	for _, d := range Directions {
		s.Signals[d] = SignalEvidence{Desired: Red, Actual: Unknown}
	}
	e.request(s, WaitRed, "", Red, now, &out)
	out.Record("RECOVERY_STARTED", now, nil)
	return out, nil
}

// Restart preserves logical traffic but invalidates physical evidence and timers.
func (e Engine) Restart(s *State, now time.Time) Effects {
	out := Effects{}
	now = s.Time(now)
	if s.Pending != nil && s.Pending.Status == "PENDING" {
		s.Pending.Status = "SUPERSEDED"
		for i := range s.Pending.Commands {
			if s.Pending.Commands[i].Status == "PENDING" {
				s.Pending.Commands[i].Status = "SUPERSEDED"
			}
		}
		out.Batches = append(out.Batches, *s.Pending)
	}
	s.Runtime.Generation++ // Fence any work from the old process even before a new batch.
	s.Runtime.Stage = Recovering
	s.Runtime.RecoveryRequired = true
	s.Runtime.Mode = Failure
	s.Runtime.CurrentPhase = ""
	s.Runtime.TargetPhase = ""
	s.Runtime.Deadline = nil
	s.Runtime.Fault = ""
	for _, d := range Directions {
		s.Signals[d] = SignalEvidence{Desired: Red, Actual: Unknown}
	}
	// Fresh device health is required after a process restart.
	for key, device := range s.Devices {
		device.Status = "UNKNOWN"
		device.ReportedAt = nil
		s.Devices[key] = device
	}
	out.Record("BACKEND_RESTART_RECOVERY", now, map[string]any{"generation": s.Runtime.Generation})
	return out
}

type Feedback struct {
	CommandID  string     `json:"command_id"`
	JunctionID string     `json:"junction_id"`
	Status     string     `json:"status"`
	Actual     Signal     `json:"actual_state"`
	Timestamp  *time.Time `json:"timestamp,omitempty"`
}

func (e Engine) Acknowledge(s *State, known Command, feedback Feedback, now time.Time) (Effects, string, error) {
	out := Effects{}
	if feedback.CommandID != known.ID || feedback.JunctionID != s.Config.ID || known.JunctionID != s.Config.ID {
		return out, "rejected", invalid("feedback does not match command and junction")
	}
	if feedback.Status != "ACK" && feedback.Status != "NACK" {
		return out, "rejected", invalid("controller status must be ACK or NACK")
	}
	if feedback.Actual != Red && feedback.Actual != Yellow && feedback.Actual != Green && feedback.Actual != Unknown {
		return out, "rejected", invalid("invalid actual signal state")
	}
	if feedback.Timestamp != nil && feedback.Timestamp.IsZero() {
		return out, "rejected", invalid("controller timestamp cannot be zero")
	}
	now = s.Time(now)
	out.Audit = append(out.Audit, Audit{Type: "CONTROLLER_ACKNOWLEDGEMENT", At: now, Direction: known.Direction, CommandID: known.ID, Details: map[string]any{"status": feedback.Status, "actual_state": feedback.Actual}})
	if known.Status == "ACK" && feedback.Status == "ACK" && feedback.Actual == known.Requested {
		out.Record("DUPLICATE_ACK", now, map[string]any{"command_id": known.ID})
		return out, "duplicate", nil
	}
	if known.Status == "ACK" && feedback.Actual != known.Requested {
		e.Fail(s, "CONTROLLER_STATE_MISMATCH", now, &out)
		return out, "fault", nil
	}
	current := s.Pending != nil && s.Pending.ID == known.BatchID && s.Pending.Status == "PENDING" && known.Generation == s.Runtime.Generation
	if !current {
		out.Record("STALE_ACK", now, map[string]any{"command_id": known.ID})
		if feedback.Actual == Green || feedback.Actual == Yellow || feedback.Actual == Unknown || feedback.Status == "NACK" {
			e.Fail(s, "STALE_CONTROLLER_STATE", now, &out)
		}
		return out, "stale", nil
	}
	if !now.Before(s.Pending.Deadline) {
		e.timeout(s, now, &out)
		return out, "late", nil
	}
	if feedback.Status == "NACK" || feedback.Actual != known.Requested {
		e.Fail(s, "CONTROLLER_STATE_MISMATCH", now, &out)
		return out, "fault", nil
	}
	index := -1
	for i, command := range s.Pending.Commands {
		if command.ID == known.ID {
			index = i
			break
		}
	}
	if index < 0 {
		return out, "rejected", invalid("command absent from active batch")
	}
	s.Pending.Commands[index].Status = "ACK"
	s.Pending.Commands[index].AcknowledgedAt = &now
	s.Signals[known.Direction] = SignalEvidence{Desired: known.Requested, Actual: feedback.Actual, CommandID: known.ID, ConfirmedAt: &now}
	out.Audit = append(out.Audit, Audit{Type: "SIGNAL_STATE_CONFIRMED", At: now, Direction: known.Direction, CommandID: known.ID, Details: map[string]any{"actual_state": feedback.Actual}})
	complete := true
	for _, command := range s.Pending.Commands {
		if command.Status != "ACK" {
			complete = false
		}
	}
	if complete {
		s.Pending.Status = "ACK"
		s.Pending.CompletedAt = &now
		switch s.Runtime.Stage {
		case WaitRed:
			s.Runtime.RecoveryRequired = false
			s.Runtime.Stage = AllRedHold
			s.Runtime.CurrentPhase = ""
			deadline := now.Add(seconds(s.Config.Policy.ClearanceSeconds))
			s.Runtime.Deadline = &deadline
			s.Runtime.Fault = ""
			if len(s.Alerts) > 0 {
				out.Record("ALERTS_RESOLVED", now, nil)
				s.Alerts = map[string]Alert{}
			}
		case WaitYellow:
			s.Runtime.Stage = YellowHold
			deadline := now.Add(seconds(s.Config.Policy.YellowSeconds))
			s.Runtime.Deadline = &deadline
		case WaitGreen:
			s.Runtime.Stage = SteadyGreen
			s.Runtime.CurrentPhase = s.Pending.Phase
			if s.Runtime.LastServed == nil {
				s.Runtime.LastServed = map[string]time.Time{}
			}
			s.Runtime.LastServed[s.Pending.Phase] = now
			deadline := now.Add(seconds(s.Config.Policy.GreenSeconds))
			s.Runtime.Deadline = &deadline
		case FailureStop:
			// A stop ACK establishes red evidence, not permission to resume.
		}
		out.Record("BATCH_CONFIRMED", now, map[string]any{"batch_id": s.Pending.ID, "stage": s.Runtime.Stage})
	}
	out.Batches = append(out.Batches, *s.Pending)
	if err := s.AssertSafe(); err != nil {
		e.Fail(s, "UNSAFE_CONTROLLER_STATE", now, &out)
	}
	appendEffects(&out, e.Tick(s, now))
	return out, "accepted", nil
}

func (e Engine) timeout(s *State, now time.Time, out *Effects) {
	if s.Pending == nil || s.Pending.Status != "PENDING" {
		return
	}
	s.Pending.Status = "TIMED_OUT"
	for i := range s.Pending.Commands {
		if s.Pending.Commands[i].Status == "PENDING" {
			s.Pending.Commands[i].Status = "TIMED_OUT"
		}
	}
	out.Batches = append(out.Batches, *s.Pending)
	out.Record("CONTROLLER_TIMEOUT", now, map[string]any{"batch_id": s.Pending.ID})
	e.Fail(s, "CONTROLLER_TIMEOUT", now, out)
}

func (e Engine) Tick(s *State, now time.Time) Effects {
	out := Effects{}
	now = s.Time(now)
	e.expire(s, now, &out)
	if s.Pending != nil && s.Pending.Status == "PENDING" && !now.Before(s.Pending.Deadline) {
		e.timeout(s, now, &out)
		return out
	}
	if s.Runtime.Stage == FailureStop {
		return out
	}
	if !s.Healthy() {
		if s.Runtime.Stage != Recovering {
			e.Fail(s, "DEVICE_UNAVAILABLE", now, &out)
		}
		return out
	}
	if s.Runtime.Stage == Recovering {
		recovered, _ := e.Recover(s, now)
		return recovered
	}
	phase, mode := s.Select(now)
	if mode != s.Runtime.Mode {
		out.Record("MODE_CHANGED", now, map[string]any{"previous_mode": s.Runtime.Mode, "mode": mode})
		s.Runtime.Mode = mode
	}
	s.Runtime.TargetPhase = phase
	switch s.Runtime.Stage {
	case AllRedHold:
		if phase != "" && s.Runtime.Deadline != nil && !now.Before(*s.Runtime.Deadline) {
			if !s.AllActual(Red) {
				e.Fail(s, "RED_CLEARANCE_UNCONFIRMED", now, &out)
				break
			}
			e.request(s, WaitGreen, phase, Green, now, &out)
		}
	case SteadyGreen:
		if phase != "" && phase != s.Runtime.CurrentPhase && (mode == Emergency || mode == Manual || s.Runtime.Deadline != nil && !now.Before(*s.Runtime.Deadline)) {
			e.request(s, WaitYellow, s.Runtime.CurrentPhase, Yellow, now, &out)
		}
	case YellowHold:
		if s.Runtime.Deadline != nil && !now.Before(*s.Runtime.Deadline) {
			e.request(s, WaitRed, "", Red, now, &out)
		}
	}
	return out
}

func (s *State) AllActual(color Signal) bool {
	for _, d := range Directions {
		evidence, exists := s.Signals[d]
		if !exists || evidence.Actual != color || evidence.ConfirmedAt == nil {
			return false
		}
	}
	return true
}

func (s *State) AssertSafe() error {
	if err := s.Config.Validate(); err != nil {
		return err
	}
	if len(s.Signals) != 4 {
		return fmt.Errorf("junction requires four signal states")
	}
	for _, field := range []string{"desired", "actual"} {
		active := ""
		for _, d := range Directions {
			v, exists := s.Signals[d]
			if !exists {
				return fmt.Errorf("missing %s signal", d)
			}
			color := v.Desired
			if field == "actual" {
				color = v.Actual
			}
			if color != Red && color != Yellow && color != Green && !(field == "actual" && color == Unknown) {
				return fmt.Errorf("invalid %s signal %s", field, color)
			}
			if color == Green || color == Yellow {
				phase := s.Config.PhaseFor(d)
				if active != "" && active != phase {
					return fmt.Errorf("conflicting %s permissive signals", field)
				}
				active = phase
			}
		}
	}
	if s.Runtime.Stage == SteadyGreen {
		for _, d := range Directions {
			if s.Signals[d].Actual != s.Signals[d].Desired || s.Signals[d].ConfirmedAt == nil {
				return fmt.Errorf("steady green lacks physical confirmation")
			}
		}
	}
	return nil
}
