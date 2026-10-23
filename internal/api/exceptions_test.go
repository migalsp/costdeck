package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
)

func send(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(method, path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)
	return rr
}

func seedCalendars(t *testing.T, server *Server, calendars ...finopsv1.HolidayCalendar) {
	t.Helper()
	cfg := &finopsv1.CostDeckConfig{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"}, Spec: finopsv1.CostDeckConfigSpec{Calendars: calendars}}
	if err := server.Client.Create(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsCalendars(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server)

	ok := `{"calendars":[{"name":"de-holidays","timezone":"Europe/Berlin","days":[{"date":"2026-12-26","name":"Boxing Day"},{"date":"2026-12-25","name":"Christmas"}]}]}`
	if rr := send(t, server, "PUT", "/api/settings", ok); rr.Code != http.StatusOK {
		t.Fatalf("PUT returned %d: %s", rr.Code, rr.Body.String())
	}
	rr := send(t, server, "GET", "/api/settings", "")
	var got struct {
		Calendars []finopsv1.HolidayCalendar `json:"calendars"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Calendars) != 1 || got.Calendars[0].Days[0].Date != "2026-12-25" {
		t.Errorf("calendars = %+v, want one calendar with its days sorted", got.Calendars)
	}

	for name, body := range map[string]string{
		"bad name":       `{"calendars":[{"name":"DE Holidays","days":[]}]}`,
		"duplicate":      `{"calendars":[{"name":"a","days":[]},{"name":"a","days":[]}]}`,
		"bad date":       `{"calendars":[{"name":"a","days":[{"date":"25.12.2026"}]}]}`,
		"date twice":     `{"calendars":[{"name":"a","days":[{"date":"2026-12-25"},{"date":"2026-12-25"}]}]}`,
		"bad timezone":   `{"calendars":[{"name":"a","timezone":"Mars/Base","days":[]}]}`,
		"bad state":      `{"calendars":[{"name":"a","state":"Maybe","days":[]}]}`,
		"impossible day": `{"calendars":[{"name":"a","days":[{"date":"2026-02-30"}]}]}`,
	} {
		if rr := send(t, server, "PUT", "/api/settings", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: PUT returned %d, want 400 (%s)", name, rr.Code, rr.Body.String())
		}
	}
}

func TestSchedulesRejectExceptionsThatWouldNotApply(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server, finopsv1.HolidayCalendar{Name: "us", Days: []finopsv1.CalendarDay{{Date: "2026-11-26"}}})

	bad := `{"metadata":{"name":"shop"},"spec":{"category":"dev","namespaces":["shop"],"calendars":["usa"],
		"exceptions":[{"name":"Release","start":"2026-11-08","end":"2026-11-07","state":"Up"}]}}`
	rr := send(t, server, "POST", "/api/scaling/groups", bad)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `calendar \"usa\" does not exist`) || !strings.Contains(rr.Body.String(), "ends before it starts") {
		t.Fatalf("POST returned %d %s, want 400 naming both problems", rr.Code, rr.Body.String())
	}

	good := `{"metadata":{"name":"shop"},"spec":{"category":"dev","namespaces":["shop"],"calendars":["us"],
		"exceptions":[{"name":"Release","start":"2026-11-07","end":"2026-11-08","state":"Up"}]}}`
	if rr := send(t, server, "POST", "/api/scaling/groups", good); rr.Code != http.StatusCreated {
		t.Fatalf("POST returned %d: %s", rr.Code, rr.Body.String())
	}
	if rr := send(t, server, "PUT", "/api/scaling/groups/shop", strings.Replace(good, `["us"]`, `["nope"]`, 1)); rr.Code != http.StatusBadRequest {
		t.Errorf("PUT with a missing calendar returned %d, want 400", rr.Code)
	}

	cfg := `{"metadata":{"name":"shop-dev"},"spec":{"targetNamespace":"shop-dev","exceptions":[{"start":"2026-11-07","state":"Sideways"}]}}`
	if rr := send(t, server, "POST", "/api/scaling/configs", cfg); rr.Code != http.StatusBadRequest {
		t.Errorf("POST config with a bad state returned %d, want 400", rr.Code)
	}
}

// "Until the next change" ends where the timetable changes, and an exception is part of it.
func TestManualOverrideUntilNextChangeStopsAtAnException(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	group := &finopsv1.ScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "costdeck"},
		Spec: finopsv1.ScalingGroupSpec{
			Namespaces: []string{"shop"},
			// Up around the clock: only the exception can end the override.
			Schedules:  []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}},
			Exceptions: []finopsv1.ScheduleException{{Name: "Holiday", Start: tomorrow, State: "Down"}},
		},
	}
	if err := server.Client.Create(context.Background(), group); err != nil {
		t.Fatal(err)
	}
	if rr := postManual(t, server, "shop", `{"active": true, "until": "nextTransition"}`); rr.Code != http.StatusOK {
		t.Fatalf("POST /manual returned %d: %s", rr.Code, rr.Body.String())
	}
	got := fetchGroup(t, server, "shop")
	want, _ := time.Parse("2006-01-02", tomorrow)
	if got.Spec.ActiveUntil == nil || !got.Spec.ActiveUntil.Time.Equal(want) {
		t.Errorf("activeUntil = %v, want the start of tomorrow's holiday %v", got.Spec.ActiveUntil, want)
	}
}

func TestExceptionTools(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server, finopsv1.HolidayCalendar{Name: "us", Days: []finopsv1.CalendarDay{{Date: "2001-01-01"}, {Date: "2999-11-26", Name: "Thanksgiving"}}})
	seedGroup(t, server, "shop")
	admin := as(auth.RoleAdmin)

	if _, err := server.executeMutatingTool(as(auth.RoleOperator), "add_schedule_exception",
		map[string]any{"target": "group", "name": "shop", "state": "Up", "start": "2999-11-07"}); !errors.Is(err, errAdminOnly) {
		t.Fatalf("an operator changed a schedule: %v", err)
	}

	out, err := server.executeMutatingTool(admin, "add_schedule_exception",
		map[string]any{"target": "group", "name": "shop", "state": "Up", "start": "2999-11-07", "end": "2999-11-08", "label": "Release"})
	if err != nil || !strings.Contains(out, "kept up from 2999-11-07 to 2999-11-08 (Release)") {
		t.Fatalf("add = %q, %v", out, err)
	}
	if got := fetchGroup(t, server, "shop").Spec.Exceptions; len(got) != 1 || got[0].Name != "Release" || got[0].State != "Up" {
		t.Fatalf("exceptions = %+v", got)
	}

	var bad errBadOverride
	if _, err := server.executeMutatingTool(admin, "add_schedule_exception",
		map[string]any{"target": "group", "name": "shop", "state": "Down", "start": "2999-12-25", "end": "2999-12-01"}); !errors.As(err, &bad) {
		t.Errorf("a backwards exception was saved: %v", err)
	}
	if _, err := server.executeMutatingTool(admin, "remove_schedule_exception",
		map[string]any{"target": "group", "name": "shop", "exception": "nothing-like-this"}); !errors.As(err, &bad) {
		t.Errorf("removing an unknown exception did not fail: %v", err)
	}
	if out, err := server.executeMutatingTool(admin, "remove_schedule_exception",
		map[string]any{"target": "group", "name": "shop", "exception": "release"}); err != nil || !strings.Contains(out, "removed 1") {
		t.Fatalf("remove = %q, %v", out, err)
	}
	if got := fetchGroup(t, server, "shop").Spec.Exceptions; len(got) != 0 {
		t.Errorf("exceptions after removal = %+v", got)
	}

	list, err := server.toolListCalendars(context.Background())
	if err != nil || !strings.Contains(list, `"name":"us"`) || !strings.Contains(list, "Thanksgiving") || strings.Contains(list, "2001-01-01") {
		t.Errorf("calendars = %s, %v; want only the days to come", list, err)
	}
}
