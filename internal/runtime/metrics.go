package runtime

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/seanlee0923/ocpp/csms"
)

type runtimeMetrics struct {
	commandRequests   *prometheus.CounterVec
	commandDuration   prometheus.Histogram
	ocppEvents        *prometheus.CounterVec
	ocppEventDuration *prometheus.HistogramVec
	handler           http.Handler
}

func newRuntimeMetrics(sessionCount func() int) *runtimeMetrics {
	registry := prometheus.NewRegistry()

	sessionsActive := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "csms_sessions_active",
		Help: "Current active OCPP sessions.",
	}, func() float64 {
		if sessionCount == nil {
			return 0
		}
		return float64(sessionCount())
	})

	commandRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "csms_command_http_requests_total",
		Help: "Command API requests by HTTP outcome class.",
	}, []string{"class"})

	commandDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "csms_command_duration_seconds",
		Help:    "Command API request duration.",
		Buckets: prometheus.DefBuckets,
	})

	// Identity is deliberately not a label here: a fleet of thousands of
	// charging stations would create thousands of series per metric. See
	// csms.MetricEvent.Identity's doc comment.
	ocppEvents := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "csms_ocpp_events_total",
		Help: "Total OCPP protocol events observed, by type/action/version/error_code.",
	}, []string{"type", "action", "version", "error_code"})

	ocppEventDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "csms_ocpp_event_duration_seconds",
		Help:    "Duration reported by terminal OCPP protocol events.",
		Buckets: prometheus.DefBuckets,
	}, []string{"type", "action", "version"})

	registry.MustRegister(sessionsActive, commandRequests, commandDuration, ocppEvents, ocppEventDuration)

	return &runtimeMetrics{
		commandRequests:   commandRequests,
		commandDuration:   commandDuration,
		ocppEvents:        ocppEvents,
		ocppEventDuration: ocppEventDuration,
		handler:           promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
	}
}

func (metrics *runtimeMetrics) commandMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		metrics.commandRequests.WithLabelValues(statusClass(recorder.status)).Inc()
		metrics.commandDuration.Observe(time.Since(started).Seconds())
	})
}

func statusClass(status int) string {
	switch status / 100 {
	case 2:
		return "2xx"
	case 4:
		return "4xx"
	case 5:
		return "5xx"
	default:
		return "other"
	}
}

func (metrics *runtimeMetrics) serveHTTP(response http.ResponseWriter, request *http.Request) {
	metrics.handler.ServeHTTP(response, request)
}

// knownOCPPActions bounds the "action" label to Actions this Runtime
// actually registers a handler for. csms.MetricEvent.Action is taken
// verbatim from the incoming OCPP-J CALL frame before router lookup
// validates it, so without this allow-list any WebSocket client (no
// authentication at Security Profile 0) could mint an unbounded number of
// Prometheus time series by sending CALL frames with arbitrary Action
// strings, growing csms_ocpp_events_total without bound.
var knownOCPPActions = map[string]bool{
	"BootNotification":   true,
	"Heartbeat":          true,
	"StatusNotification": true,
	resetAction:          true,
}

// recordOCPPEvent is wired as csms.Config.Metrics so protocol-level events
// (session lifecycle, inbound CALL/SEND, outbound Call) are observable
// without reimplementing csms.Server's internal bookkeeping.
func (metrics *runtimeMetrics) recordOCPPEvent(_ context.Context, event csms.MetricEvent) {
	eventType := ocppEventLabel(event.Type)
	version := string(event.Version)
	action := event.Action
	if !knownOCPPActions[action] {
		action = "unknown"
	}
	metrics.ocppEvents.WithLabelValues(eventType, action, version, string(event.ErrorCode)).Inc()
	if event.Duration > 0 {
		metrics.ocppEventDuration.WithLabelValues(eventType, action, version).Observe(event.Duration.Seconds())
	}
}

func ocppEventLabel(eventType csms.MetricEventType) string {
	switch eventType {
	case csms.MetricSessionConnected:
		return "session_connected"
	case csms.MetricSessionDisconnected:
		return "session_disconnected"
	case csms.MetricCallReceived:
		return "call_received"
	case csms.MetricCallCompleted:
		return "call_completed"
	case csms.MetricCallRejected:
		return "call_rejected"
	case csms.MetricSendReceived:
		return "send_received"
	case csms.MetricSendCompleted:
		return "send_completed"
	case csms.MetricSendDropped:
		return "send_dropped"
	case csms.MetricOutboundCallRejected:
		return "outbound_call_rejected"
	case csms.MetricOutboundCallSent:
		return "outbound_call_sent"
	case csms.MetricOutboundCallCompleted:
		return "outbound_call_completed"
	case csms.MetricOutboundCallFailed:
		return "outbound_call_failed"
	case csms.MetricOutboundCallTimeout:
		return "outbound_call_timeout"
	case csms.MetricOutboundCallCanceled:
		return "outbound_call_canceled"
	default:
		return "unknown"
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}
