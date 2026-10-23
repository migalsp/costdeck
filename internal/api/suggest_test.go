package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestScheduleSuggestionWithoutHistory(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	rr := send(t, server, "GET", "/api/namespaces/shop-dev/schedule-suggestion?timezone=Europe/Berlin", "")
	var got suggestionResponse
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	if got.Reason != "insufficient" || got.Source != "costdeck" || got.Timezone != "Europe/Berlin" || got.Namespace != "shop-dev" || got.Schedules == nil {
		t.Errorf("suggestion = %+v", got)
	}
	if rr := send(t, server, "GET", "/api/namespaces/shop-dev/schedule-suggestion?timezone=Mars/Olympus", ""); rr.Code != http.StatusBadRequest {
		t.Errorf("unknown timezone: %d %s", rr.Code, rr.Body.String())
	}
	out, err := server.toolSuggestSchedule(context.Background(), "shop-dev", "")
	if err != nil || !strings.Contains(out, "connect VictoriaMetrics") || !strings.Contains(out, `"timezone":"UTC"`) {
		t.Errorf("tool = %s (%v)", out, err)
	}
}
