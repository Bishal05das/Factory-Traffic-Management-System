package domain

import (
	"sort"
	"time"
)

func (e Engine) expire(s *State, now time.Time, out *Effects) {
	if s.Manual != nil && !now.Before(s.Manual.ExpiresAt) {
		out.Record("MANUAL_EXPIRED", now, map[string]any{"intent_id": s.Manual.ID})
		s.Manual = nil
	}
	for id, vehicle := range s.Vehicles {
		if vehicle.EmergencyActive && vehicle.EmergencyExpiresAt != nil && !now.Before(*vehicle.EmergencyExpiresAt) {
			vehicle.EmergencyActive = false
			s.Vehicles[id] = vehicle
			code := "EMERGENCY_EXPIRED_" + id
			s.Alerts[code] = Alert{Code: code, Message: "Emergency priority expired; vehicle remains queued", OpenedAt: now}
			out.Record("EMERGENCY_EXPIRED", now, map[string]any{"vehicle_id": id})
		}
	}
}

// Select chooses intent only. Tick is the only operation that translates intent
// into transitions, so scheduler priority cannot bypass the clearance stages.
func (s *State) Select(now time.Time) (string, Mode) {
	if s.Runtime.RecoveryRequired || s.Runtime.Fault != "" || s.Runtime.Stage == FailureStop || s.Runtime.Stage == Recovering {
		return "", Failure
	}
	var emergency *Vehicle
	for _, vehicle := range s.Vehicles {
		if !vehicle.EmergencyActive || vehicle.EmergencyExpiresAt == nil || !now.Before(*vehicle.EmergencyExpiresAt) {
			continue
		}
		if emergency == nil || vehicle.AcceptedAt.Before(emergency.AcceptedAt) || vehicle.AcceptedAt.Equal(emergency.AcceptedAt) && vehicle.ArrivalEventID < emergency.ArrivalEventID {
			copy := vehicle
			emergency = &copy
		}
	}
	if emergency != nil {
		return s.Config.PhaseFor(emergency.Direction), Emergency
	}
	if s.Manual != nil && now.Before(s.Manual.ExpiresAt) {
		return s.Manual.Phase, Manual
	}
	type score struct {
		phase      string
		total      float64
		oldest     time.Time
		hasTraffic bool
	}
	scores := make([]score, 0, len(s.Config.Phases))
	var oldest *Vehicle
	var oldestUnserved time.Time
	for _, phase := range s.Config.Phases {
		candidate := score{phase: phase.ID}
		for _, vehicle := range s.Vehicles {
			if !contains(phase.Directions, vehicle.Direction) {
				continue
			}
			age := now.Sub(vehicle.AcceptedAt).Seconds()
			if age < 0 {
				age = 0
			}
			candidate.total += float64(s.Config.Policy.Weights[vehicle.Type]) + age/float64(s.Config.Policy.AgeStepSeconds)
			candidate.hasTraffic = true
			if candidate.oldest.IsZero() || vehicle.AcceptedAt.Before(candidate.oldest) {
				candidate.oldest = vehicle.AcceptedAt
			}
			unservedSince := vehicle.AcceptedAt
			if served := s.Runtime.LastServed[phase.ID]; served.After(unservedSince) {
				unservedSince = served
			}
			// A phase that is currently confirmed green is being served now,
			// even if sensors have not yet reported vehicle clearances.
			if s.Runtime.Stage == SteadyGreen && phase.ID == s.Runtime.CurrentPhase {
				unservedSince = now
			}
			if oldest == nil || unservedSince.Before(oldestUnserved) || unservedSince.Equal(oldestUnserved) && vehicle.ArrivalEventID < oldest.ArrivalEventID {
				copy := vehicle
				oldest = &copy
				oldestUnserved = unservedSince
			}
		}
		if candidate.hasTraffic {
			scores = append(scores, candidate)
		}
	}
	if oldest != nil && now.Sub(oldestUnserved) >= seconds(s.Config.Policy.StarvationSeconds) {
		return s.Config.PhaseFor(oldest.Direction), Automatic
	}
	if len(scores) == 0 {
		return s.Runtime.CurrentPhase, Automatic
	}
	sort.Slice(scores, func(i, j int) bool {
		a, b := scores[i], scores[j]
		if a.total != b.total {
			return a.total > b.total
		}
		if a.phase == s.Runtime.CurrentPhase {
			return true
		}
		if b.phase == s.Runtime.CurrentPhase {
			return false
		}
		if !a.oldest.Equal(b.oldest) {
			return a.oldest.Before(b.oldest)
		}
		return a.phase < b.phase
	})
	return scores[0].phase, Automatic
}
