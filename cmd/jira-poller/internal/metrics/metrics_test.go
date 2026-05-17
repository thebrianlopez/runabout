package metrics_test

import (
	"testing"

	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// freshReg returns a new isolated Prometheus registry for each test.
func freshReg() *prometheus.Registry {
	return prometheus.NewRegistry()
}

// gather returns the metric families from reg, panicking on error.
func gather(t *testing.T, reg *prometheus.Registry) map[string]*dto.MetricFamily {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	m := make(map[string]*dto.MetricFamily, len(mfs))
	for _, mf := range mfs {
		m[mf.GetName()] = mf
	}
	return m
}

// CT-1: Register succeeds with fresh registry.
func TestRegister_CT1_SucceedsOnFreshRegistry(t *testing.T) {
	reg := freshReg()
	if err := metrics.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

// CT-2: All 7 metric families are present after Register.
func TestRegister_CT2_AllSevenFamiliesPresent(t *testing.T) {
	reg := freshReg()
	if err := metrics.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}

	want := []string{
		"jira_poller_poll_duration_seconds",
		"jira_poller_issues_returned_total",
		"jira_poller_transitions_published_total",
		"jira_poller_transitions_deduped_total",
		"jira_poller_errors_total",
		"jira_poller_jira_api_requests_total",
		"jira_poller_outbox_dead_total",
	}

	// Trigger at least one observation so histograms/counters appear in gather.
	metrics.PollDuration.With(prometheus.Labels{"result": "success"}).Observe(0.1)
	metrics.IssuesReturnedTotal.Add(1)
	metrics.TransitionsPublishedTotal.With(prometheus.Labels{"project": "INFRA", "to_status": "Done"}).Inc()
	metrics.TransitionsDedupedTotal.Inc()
	metrics.ErrorsTotal.With(prometheus.Labels{"stage": "jira"}).Inc()
	metrics.JiraAPIRequestsTotal.With(prometheus.Labels{"status_code": "200"}).Inc()
	metrics.OutboxDeadTotal.Inc()

	families := gather(t, reg)
	for _, name := range want {
		if _, ok := families[name]; !ok {
			t.Errorf("metric family %q not found in registry", name)
		}
	}
	if len(families) != 7 {
		t.Errorf("expected 7 families, got %d", len(families))
	}
}

// CT-3: PollDuration.Observe does not panic; count == 1 after one observation.
func TestRegister_CT3_PollDurationObserve(t *testing.T) {
	reg := freshReg()
	if err := metrics.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}

	metrics.PollDuration.With(prometheus.Labels{"result": "success"}).Observe(1.5)

	families := gather(t, reg)
	mf, ok := families["jira_poller_poll_duration_seconds"]
	if !ok {
		t.Fatal("poll_duration metric not found")
	}
	if len(mf.Metric) == 0 {
		t.Fatal("no metric series for poll_duration")
	}
	if mf.Metric[0].GetHistogram().GetSampleCount() != 1 {
		t.Errorf("sample count = %d, want 1", mf.Metric[0].GetHistogram().GetSampleCount())
	}
}

// CT-4: ErrorsTotal.With(stage="dedupe").Inc() increments the correct label series.
func TestRegister_CT4_ErrorsTotalDedupeLabel(t *testing.T) {
	reg := freshReg()
	if err := metrics.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}

	metrics.ErrorsTotal.With(prometheus.Labels{"stage": "dedupe"}).Inc()

	families := gather(t, reg)
	mf, ok := families["jira_poller_errors_total"]
	if !ok {
		t.Fatal("errors_total metric not found")
	}
	for _, m := range mf.Metric {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "stage" && lp.GetValue() == "dedupe" {
				if m.GetCounter().GetValue() != 1 {
					t.Errorf("errors_total{stage=dedupe} = %f, want 1", m.GetCounter().GetValue())
				}
				return
			}
		}
	}
	t.Error("errors_total{stage=dedupe} series not found")
}

// CT-13: Register returns error on double-registration with same registry.
func TestRegister_CT13_DoubleRegisterReturnsError(t *testing.T) {
	reg := freshReg()
	if err := metrics.Register(reg); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	// Reset package vars to nil so the second Register call creates new instances
	// that conflict with the already-registered collectors.
	// (The test relies on the fact that the same metric names are re-used.)
	err := metrics.Register(reg)
	if err == nil {
		t.Error("second Register should return error for duplicate metrics")
	}
}
