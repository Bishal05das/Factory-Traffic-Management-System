package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"factorytraffic/internal/adapters/controller"
	httpadapter "factorytraffic/internal/adapters/http"
	"factorytraffic/internal/adapters/postgres"
	"factorytraffic/internal/application"
	"factorytraffic/internal/domain"
	"factorytraffic/internal/ports"
)

type apiTest struct {
	t       *testing.T
	store   *postgres.Store
	app     *application.Service
	handler http.Handler
	id      string
}

func testAPI(t *testing.T) *apiTest {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for API integration tests")
	}
	store, err := postgres.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	app := application.New(store, log)
	a := &apiTest{t: t, store: store, app: app, id: "api-" + application.NewID(), handler: (httpadapter.Server{App: app, Controller: controller.REST{Store: store}, Log: log}).Handler()}
	a.call("POST", "/api/junctions", application.FactoryConfig(a.id, "API test"), 201)
	return a
}

func (a *apiTest) call(method, path string, value any, want int) []byte {
	a.t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		a.t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	if w.Code != want {
		a.t.Fatalf("%s %s: wanted %d, got %d: %s", method, path, want, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func (a *apiTest) online() {
	for _, kind := range []string{"SIGNAL_CONTROLLER", "SIGNAL", "SENSOR"} {
		directions := domain.Directions
		if kind == "SIGNAL_CONTROLLER" {
			directions = []domain.Direction{""}
		}
		for _, d := range directions {
			a.call("POST", "/api/device-events", domain.DeviceEvent{ID: application.NewID(), JunctionID: a.id, Type: kind, Direction: d, Status: "ONLINE", Timestamp: time.Now().UTC()}, 201)
		}
	}
	a.ack()
}

func (a *apiTest) ack() {
	var feed ports.CommandFeed
	if err := json.Unmarshal(a.call("GET", "/api/junctions/"+a.id+"/controller-commands", nil, 200), &feed); err != nil {
		a.t.Fatal(err)
	}
	for _, c := range feed.Commands {
		a.call("POST", "/api/controller-events", domain.Feedback{CommandID: c.ID, JunctionID: a.id, Status: "ACK", Actual: c.Requested}, 200)
	}
}

func (a *apiTest) status() application.Status {
	var status application.Status
	if err := json.Unmarshal(a.call("GET", "/api/junctions/"+a.id+"/status", nil, 200), &status); err != nil {
		a.t.Fatal(err)
	}
	return status
}

func (a *apiTest) expireHold() {
	if _, err := a.store.DB.Exec("UPDATE junction_runtime SET stage_deadline=clock_timestamp()-interval '1 second' WHERE junction_id=$1", a.id); err != nil {
		a.t.Fatal(err)
	}
	if err := a.app.Tick(context.Background(), a.id); err != nil {
		a.t.Fatal(err)
	}
}

func TestAPIDuplicateClearanceAndEmergencySequence(t *testing.T) {
	a := testAPI(t)
	a.online()
	arrival := domain.SensorEvent{ID: application.NewID(), JunctionID: a.id, Direction: domain.North, Type: "VEHICLE_ARRIVED", VehicleID: "truck", VehicleType: domain.Truck, Sequence: 1, Timestamp: time.Now().UTC()}
	a.call("POST", "/api/sensor-events", arrival, 201)
	a.call("POST", "/api/sensor-events", arrival, 200)
	if a.status().Queues[domain.North] != 1 {
		t.Fatal("duplicate incremented queue")
	}
	changed := arrival
	changed.VehicleID = "different"
	a.call("POST", "/api/sensor-events", changed, 409)
	a.expireHold()
	a.ack()
	if a.status().Phase != "NORTH_SOUTH" {
		t.Fatal("north did not become confirmed green")
	}
	emergency := arrival
	emergency.ID = application.NewID()
	emergency.Direction = domain.East
	emergency.VehicleID = "ambulance"
	emergency.VehicleType = domain.EmergencyVehicle
	a.call("POST", "/api/sensor-events", emergency, 201)
	if status := a.status(); status.Stage != domain.WaitYellow || status.Desired[domain.East] != domain.Red {
		t.Fatal("emergency skipped yellow")
	}
	a.ack()
	a.expireHold()
	a.ack()
	a.expireHold()
	a.ack()
	if status := a.status(); status.Phase != "EAST_WEST" || status.Actual[domain.East] != domain.Green || status.Queues[domain.East] != 1 {
		t.Fatal("emergency clearance sequence failed")
	}
	clear := emergency
	clear.ID = application.NewID()
	clear.Type = "VEHICLE_CLEARED"
	clear.VehicleType = ""
	clear.Sequence = 2
	a.call("POST", "/api/sensor-events", clear, 201)
	if status := a.status(); status.Queues[domain.East] != 0 || len(status.Emergencies) != 0 {
		t.Fatal("clearance did not remove emergency")
	}
	var history []ports.HistoryItem
	json.Unmarshal(a.call("GET", "/api/junctions/"+a.id+"/history?limit=200", nil, 200), &history)
	seen := map[string]bool{}
	for _, item := range history {
		seen[item.Type] = true
	}
	for _, kind := range []string{"DUPLICATE_EVENT", "CONFLICTING_EVENT_ID", "EMERGENCY_DETECTED", "EMERGENCY_CLEARED", "SIGNAL_STATE_CONFIRMED"} {
		if !seen[kind] {
			t.Fatalf("missing %s audit", kind)
		}
	}
}

func TestAPIConcurrentEventsStayConsistent(t *testing.T) {
	a := testAPI(t)
	a.online()
	var wg sync.WaitGroup
	failures := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			direction := domain.Directions[i%4]
			kind := domain.Truck
			if i == 1 {
				kind = domain.EmergencyVehicle
			}
			event := domain.SensorEvent{ID: fmt.Sprintf("%s-event-%d", a.id, i), JunctionID: a.id, Direction: direction, Type: "VEHICLE_ARRIVED", VehicleID: fmt.Sprintf("v-%d", i), VehicleType: kind, Sequence: int64(i + 1), Timestamp: time.Now().UTC()}
			_, err := a.app.Sensor(context.Background(), event)
			failures <- err
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := a.store.Read(context.Background(), a.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Vehicles) != 30 {
		t.Fatal("concurrent events lost queue entries")
	}
	if err = state.AssertSafe(); err != nil {
		t.Fatal(err)
	}
}

func TestAPITimeoutOfflineAndRestartRecovery(t *testing.T) {
	a := testAPI(t)
	a.online()
	a.call("POST", "/api/junctions/"+a.id+"/commands", domain.ControlRequest{Command: "MANUAL_GREEN_REQUEST", Direction: domain.West}, 202)
	a.expireHold()
	if _, err := a.store.DB.Exec("UPDATE command_batches SET deadline_at=clock_timestamp()-interval '1 second' WHERE junction_id=$1 AND status='PENDING'", a.id); err != nil {
		t.Fatal(err)
	}
	if err := a.app.Tick(context.Background(), a.id); err != nil {
		t.Fatal(err)
	}
	if status := a.status(); status.Mode != domain.Failure || status.Actual[domain.West] != domain.Unknown {
		t.Fatal("timeout assumed green execution")
	}
	a.call("POST", "/api/junctions/"+a.id+"/commands", domain.ControlRequest{Command: "RECOVER"}, 202)
	a.ack()
	if err := a.store.Transact(context.Background(), a.id, func(state *domain.State, tx ports.Transaction) error {
		return tx.Save(context.Background(), state, a.app.Engine.Restart(state, tx.Now()))
	}); err != nil {
		t.Fatal(err)
	}
	if status := a.status(); status.Stage != domain.Recovering || status.Actual[domain.West] != domain.Unknown || status.Manual == nil {
		t.Fatal("restart did not preserve intent or invalidate physical evidence")
	}
	a.online()
	if a.status().Stage != domain.AllRedHold {
		t.Fatal("fresh online/red evidence did not reconcile restart")
	}
}

func TestAPIValidationAndUnknownResources(t *testing.T) {
	a := testAPI(t)
	a.call("GET", "/api/junctions/no-such-junction/status", nil, 404)
	a.call("POST", "/api/sensor-events", map[string]string{"event_id": "missing-fields"}, 422)
	a.call("POST", "/api/junctions/"+a.id+"/commands", map[string]string{"command": "SET_GREEN"}, 422)
	a.call("POST", "/api/controller-events", domain.Feedback{CommandID: "unknown", JunctionID: a.id, Status: "ACK", Actual: domain.Green}, 404)
	a.call("GET", "/api/junctions/"+a.id+"/history?limit=201", nil, 422)
	r := httptest.NewRequest("POST", "/api/sensor-events", bytes.NewBufferString(`{"event_id":"x"} {}`))
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("multiple JSON values were accepted")
	}
}
