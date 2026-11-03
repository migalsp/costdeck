package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestSelectNamespacesPreview(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	for name, labels := range map[string]map[string]string{
		"pr-1": {"env": "preview", "team": "shop"}, "pr-2": {"env": "preview", "team": "pay"},
		"pr-3": {"env": "preview", finopsv1.OptOutLabel: "off"}, "costdeck": {"env": "preview"}, "prod": {"env": "prod"},
	} {
		if err := server.Client.Create(context.Background(), &corev1.Namespace{Name: name, Labels: labels}); err != nil {
			t.Fatal(err)
		}
	}
	preview := func(selector string) (int, map[string]any) {
		rr := send(t, server, "GET", "/api/namespace-selector?selector="+url.QueryEscape(selector), "")
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		return rr.Code, body
	}

	code, body := preview("env=preview,team in (shop,pay)")
	if code != http.StatusOK {
		t.Fatalf("preview = %d %v", code, body)
	}
	if got := body["namespaces"]; strings.Join(toStrings(got), ",") != "pr-1,pr-2" {
		t.Errorf("namespaces = %v, want pr-1,pr-2 (no opted-out or operator namespace)", got)
	}
	sel, _ := json.Marshal(body["selector"])
	if !strings.Contains(string(sel), `"matchLabels":{"env":"preview"}`) || !strings.Contains(string(sel), `"operator":"In"`) {
		t.Errorf("selector = %s, want it as a schedule stores it", sel)
	}
	if code, body := preview("nothing=matches"); code != http.StatusOK || len(toStrings(body["namespaces"])) != 0 {
		t.Errorf("no match = %d %v, want an empty list", code, body)
	}
	if code, _ := preview("env in (preview"); code != http.StatusBadRequest {
		t.Errorf("unparsable selector = %d, want 400", code)
	}
	if code := send(t, server, "GET", "/api/namespace-selector", "").Code; code != http.StatusBadRequest {
		t.Errorf("missing selector = %d, want 400", code)
	}
}

func TestGroupSelectorValidation(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server)
	empty := `{"metadata":{"name":"previews"},"spec":{"category":"dev","namespaceSelector":{}}}`
	if rr := send(t, server, "POST", "/api/scaling/groups", empty); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "every namespace") {
		t.Errorf("an empty selector = %d %s, want 400: it would select the whole cluster", rr.Code, rr.Body.String())
	}
	bad := `{"metadata":{"name":"previews"},"spec":{"category":"dev","namespaceSelector":{"matchExpressions":[{"key":"env","operator":"In"}]}}}`
	if rr := send(t, server, "POST", "/api/scaling/groups", bad); rr.Code != http.StatusBadRequest {
		t.Errorf("an invalid selector = %d, want 400", rr.Code)
	}
	ok := `{"metadata":{"name":"previews"},"spec":{"category":"dev","namespaceSelector":{"matchLabels":{"env":"preview"}}}}`
	if rr := send(t, server, "POST", "/api/scaling/groups", ok); rr.Code != http.StatusCreated {
		t.Errorf("a selector-only group = %d %s, want 201", rr.Code, rr.Body.String())
	}
}

func toStrings(v any) []string {
	items := v.([]any)
	out := make([]string, 0, len(items))
	for _, x := range items {
		out = append(out, x.(string))
	}
	return out
}
