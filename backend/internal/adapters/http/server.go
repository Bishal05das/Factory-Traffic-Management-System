package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"factorytraffic/internal/application"
	"factorytraffic/internal/domain"
	"factorytraffic/internal/ports"
)

type Server struct {
	App        *application.Service
	Controller ports.Controller
	Log        *slog.Logger
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "live"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.App.Store.Ping(ctx); err != nil {
			s.fail(w, err)
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/junctions", func(w http.ResponseWriter, r *http.Request) {
		configs, err := s.App.Store.List(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		items := []application.Status{}
		for _, c := range configs {
			status, err := s.App.Status(r.Context(), c.ID)
			if err != nil {
				s.fail(w, err)
				return
			}
			items = append(items, status)
		}
		write(w, 200, items)
	})
	mux.HandleFunc("POST /api/junctions", func(w http.ResponseWriter, r *http.Request) {
		var c domain.Config
		if !s.decode(w, r, &c) {
			return
		}
		if err := s.App.Store.Create(r.Context(), c); err != nil {
			s.fail(w, err)
			return
		}
		write(w, 201, c)
	})
	mux.HandleFunc("GET /api/junctions/{id}", func(w http.ResponseWriter, r *http.Request) {
		state, err := s.App.Store.Read(r.Context(), r.PathValue("id"))
		if err != nil {
			s.fail(w, err)
			return
		}
		write(w, 200, state.Config)
	})
	mux.HandleFunc("GET /api/junctions/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		status, err := s.App.Status(r.Context(), r.PathValue("id"))
		if err != nil {
			s.fail(w, err)
			return
		}
		write(w, 200, status)
	})
	mux.HandleFunc("POST /api/sensor-events", func(w http.ResponseWriter, r *http.Request) {
		var event domain.SensorEvent
		if !s.decode(w, r, &event) {
			return
		}
		result, err := s.App.Sensor(r.Context(), event)
		if err != nil {
			s.fail(w, err)
			return
		}
		code := 201
		if result.Outcome == "duplicate" {
			code = 200
		}
		write(w, code, result)
	})
	mux.HandleFunc("POST /api/device-events", func(w http.ResponseWriter, r *http.Request) {
		var event domain.DeviceEvent
		if !s.decode(w, r, &event) {
			return
		}
		result, err := s.App.Device(r.Context(), event)
		if err != nil {
			s.fail(w, err)
			return
		}
		code := 201
		if result.Outcome == "duplicate" {
			code = 200
		}
		write(w, code, result)
	})
	mux.HandleFunc("POST /api/junctions/{id}/commands", func(w http.ResponseWriter, r *http.Request) {
		var request domain.ControlRequest
		if !s.decode(w, r, &request) {
			return
		}
		result, err := s.App.Control(r.Context(), r.PathValue("id"), request)
		if err != nil {
			s.fail(w, err)
			return
		}
		write(w, 202, result)
	})
	mux.HandleFunc("GET /api/junctions/{id}/controller-commands", func(w http.ResponseWriter, r *http.Request) {
		feed, err := s.Controller.Pending(r.Context(), r.PathValue("id"))
		if err != nil {
			s.fail(w, err)
			return
		}
		write(w, 200, feed)
	})
	mux.HandleFunc("POST /api/controller-events", func(w http.ResponseWriter, r *http.Request) {
		var feedback domain.Feedback
		if !s.decode(w, r, &feedback) {
			return
		}
		result, err := s.App.Feedback(r.Context(), feedback)
		if err != nil {
			s.fail(w, err)
			return
		}
		write(w, 200, result)
	})
	mux.HandleFunc("GET /api/junctions/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		before := int64(0)
		limit := 50
		var err error
		if value := r.URL.Query().Get("before"); value != "" {
			before, err = strconv.ParseInt(value, 10, 64)
			if err != nil || before < 0 {
				s.fail(w, &domain.Error{Code: "invalid", Message: "before must be a nonnegative cursor"})
				return
			}
		}
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 || limit > 200 {
				s.fail(w, &domain.Error{Code: "invalid", Message: "limit must be between 1 and 200"})
				return
			}
		}
		items, err := s.App.Store.History(r.Context(), r.PathValue("id"), before, limit)
		if err != nil {
			s.fail(w, err)
			return
		}
		write(w, 200, items)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if recovered := recover(); recovered != nil {
				s.Log.Error("HTTP panic", "value", recovered)
				write(w, 500, map[string]string{"error": "internal", "message": "internal server error"})
			}
		}()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s Server) decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		s.malformed(w, r, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		s.malformed(w, r, errors.New("request must contain exactly one JSON object"))
		return false
	}
	return true
}

func (s Server) malformed(w http.ResponseWriter, r *http.Request, err error) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if auditErr := s.App.Store.Reject(ctx, r.PathValue("id"), domain.Audit{Type: "MALFORMED_REQUEST", At: time.Now().UTC(), Details: map[string]any{"path": r.URL.Path, "reason": err.Error()}}); auditErr != nil {
		s.Log.Error("request rejection audit failed", "error", auditErr)
	}
	write(w, 400, map[string]string{"error": "malformed_request", "message": err.Error()})
}

func (s Server) fail(w http.ResponseWriter, err error) {
	code, kind, message := 503, "unavailable", "backend operation unavailable"
	var typed *domain.Error
	if errors.As(err, &typed) {
		kind, message = typed.Code, typed.Message
		code = 422
		if typed.Code == "conflict" {
			code = 409
		}
	} else if errors.Is(err, ports.ErrNotFound) {
		code, kind, message = 404, "not_found", "junction or controller command not found"
	} else if errors.Is(err, ports.ErrExists) {
		code, kind, message = 409, "conflict", "junction already exists"
	} else {
		s.Log.Error("API operation failed", "error", err)
	}
	write(w, code, map[string]string{"error": kind, "message": message})
}

func write(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
