package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seanlee0923/ocpp/csms"
	"github.com/seanlee0923/ocpp/protocol"
)

func TestRuntimeMetricsExposeSessionsAndCommandOutcomes(t *testing.T) {
	metrics := newRuntimeMetrics(func() int { return 3 })
	success := metrics.commandMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	}))
	rejected := metrics.commandMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	success.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	rejected.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	response := httptest.NewRecorder()
	metrics.serveHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		"csms_sessions_active 3",
		`csms_command_http_requests_total{class="2xx"} 1`,
		`csms_command_http_requests_total{class="4xx"} 1`,
		"csms_command_duration_seconds_count 2",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics do not contain %q:\n%s", expected, body)
		}
	}
}

func TestRuntimeMetricsRecordOCPPEvents(t *testing.T) {
	metrics := newRuntimeMetrics(func() int { return 0 })
	metrics.recordOCPPEvent(context.Background(), csms.MetricEvent{
		Type: csms.MetricCallCompleted, Version: protocol.OCPP16, Action: "BootNotification",
		Duration: 5 * time.Millisecond,
	})
	metrics.recordOCPPEvent(context.Background(), csms.MetricEvent{
		Type: csms.MetricCallRejected, Version: protocol.OCPP16, Action: "BootNotification",
		ErrorCode: "InternalError",
	})

	response := httptest.NewRecorder()
	metrics.serveHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		`csms_ocpp_events_total{action="BootNotification",error_code="",type="call_completed",version="ocpp1.6"} 1`,
		`csms_ocpp_events_total{action="BootNotification",error_code="InternalError",type="call_rejected",version="ocpp1.6"} 1`,
		`csms_ocpp_event_duration_seconds_count{action="BootNotification",type="call_completed",version="ocpp1.6"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics do not contain %q:\n%s", expected, body)
		}
	}
}

func TestRuntimeMetricsCollapseUnknownActionsToBoundCardinality(t *testing.T) {
	metrics := newRuntimeMetrics(func() int { return 0 })
	for _, action := range []string{"NotARealAction", "AnotherFakeOne", "Reset; DROP metrics"} {
		metrics.recordOCPPEvent(context.Background(), csms.MetricEvent{
			Type: csms.MetricCallReceived, Version: protocol.OCPP16, Action: action,
		})
	}

	response := httptest.NewRecorder()
	metrics.serveHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	expected := `csms_ocpp_events_total{action="unknown",error_code="",type="call_received",version="ocpp1.6"} 3`
	if !strings.Contains(body, expected) {
		t.Fatalf("metrics do not contain %q (unrecognized actions must collapse to a single \"unknown\" series):\n%s", expected, body)
	}
	for _, action := range []string{"NotARealAction", "AnotherFakeOne"} {
		if strings.Contains(body, `action="`+action+`"`) {
			t.Fatalf("metrics must not expose attacker-supplied action %q as its own label series:\n%s", action, body)
		}
	}
}
