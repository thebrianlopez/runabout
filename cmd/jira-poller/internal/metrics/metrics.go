// Package metrics defines the 7 Prometheus metric vars for the jira-poller
// service. Call Register once at startup before any metric is incremented.
// Tests must pass prometheus.NewRegistry() — never use DefaultRegisterer in tests.
package metrics

import (
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// ErrMetricsInit is returned when Register is called with a registry that
// already has a jira-poller metric registered (double-init guard).
var ErrMetricsInit = errors.New("metrics: double registration detected")

// Package-level metric vars. Nil until Register is called.
var (
	// PollDuration observes poll cycle wall-clock duration in seconds.
	// Labels: result = "success" | "error"
	PollDuration *prometheus.HistogramVec

	// IssuesReturnedTotal counts issues returned by Jira across all poll pages.
	IssuesReturnedTotal prometheus.Counter

	// TransitionsPublishedTotal counts status transitions published to the outbox.
	// Labels: project (Jira project key), to_status (destination status name)
	TransitionsPublishedTotal *prometheus.CounterVec

	// TransitionsDedupedTotal counts transitions skipped because they were
	// already in the outbox (UNIQUE constraint) from a previous poll cycle.
	TransitionsDedupedTotal prometheus.Counter

	// ErrorsTotal counts poll-cycle errors by stage.
	// Labels: stage = "jira" | "dedupe" | "publish"
	ErrorsTotal *prometheus.CounterVec

	// JiraAPIRequestsTotal counts outgoing Jira API requests by HTTP status code.
	// Labels: status_code (string, e.g. "200", "429", "503")
	JiraAPIRequestsTotal *prometheus.CounterVec

	// OutboxDeadTotal counts events dead-lettered by the drain worker (age > 24h
	// or max attempts exceeded).
	OutboxDeadTotal prometheus.Counter
)

// Register initialises all metric vars and registers them with reg.
// Must be called exactly once at startup. Returns ErrMetricsInit on
// double-registration.
func Register(reg prometheus.Registerer) error {
	pd := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "jira_poller_poll_duration_seconds",
		Help:    "Duration of a single poll cycle in seconds.",
		Buckets: []float64{0.005, 0.05, 0.5, 5, 30},
	}, []string{"result"})

	irt := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "jira_poller_issues_returned_total",
		Help: "Total number of Jira issues returned across all poll pages.",
	})

	tpt := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jira_poller_transitions_published_total",
		Help: "Total status transitions published to the outbox.",
	}, []string{"project", "to_status"})

	tdt := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "jira_poller_transitions_deduped_total",
		Help: "Total transitions skipped as already-published duplicates.",
	})

	et := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jira_poller_errors_total",
		Help: "Total poll errors by stage.",
	}, []string{"stage"})

	jart := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jira_poller_jira_api_requests_total",
		Help: "Total outgoing Jira API requests by HTTP status code.",
	}, []string{"status_code"})

	odt := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "jira_poller_outbox_dead_total",
		Help: "Total events dead-lettered by the drain worker.",
	})

	for _, c := range []prometheus.Collector{pd, irt, tpt, tdt, et, jart, odt} {
		if err := reg.Register(c); err != nil {
			return fmt.Errorf("%w: %s", ErrMetricsInit, err)
		}
	}

	PollDuration = pd
	IssuesReturnedTotal = irt
	TransitionsPublishedTotal = tpt
	TransitionsDedupedTotal = tdt
	ErrorsTotal = et
	JiraAPIRequestsTotal = jart
	OutboxDeadTotal = odt

	return nil
}
