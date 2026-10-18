// These tests protect arithmetic used in published claims and socket cleanup.
// Timing and throughput thresholds belong to benchmark results, not unit tests.
package bench

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestCPUTime(t *testing.T) {
	for _, test := range []struct {
		text string
		want float64
	}{{"0:00.01", 0.01}, {"123:45.50", 7425.5}, {"01:02:03", 3723}, {"2-01:02:03", 176523}} {
		got, err := parseCPUTime(test.text)
		if err != nil || got != test.want {
			t.Fatalf("parseCPUTime(%q) = %v, %v; want %v", test.text, got, err, test.want)
		}
	}
	for _, text := range []string{"", "oops", "1:-2", "1:bad", "a-01:00:00"} {
		if _, err := parseCPUTime(text); err == nil {
			t.Fatalf("accepted malformed CPU time %q", text)
		}
	}
}

func TestSummaryRetainsSlowTrials(t *testing.T) {
	values := []float64{100, 1, 3, 2}
	original := slices.Clone(values)
	if got := metric(values); got != (Metric{Median: 2.5, Min: 1, Max: 100}) {
		t.Fatalf("metric = %+v", got)
	}
	if percentile(values, 0.50) != 2 || percentile(values, 0.95) != 100 || !slices.Equal(values, original) {
		t.Fatal("percentile definition or source observations changed")
	}
	trials := []Trial{
		{Scenario: "direct", Variant: "sertun", Single: BulkResult{MiBPerSecond: 100, Resources: Resources{TunnelCPUPercent: 100}}},
		{Scenario: "direct", Variant: "sertun", Single: BulkResult{MiBPerSecond: 1, Resources: Resources{TunnelCPUPercent: 10}}},
		{Scenario: "direct", Variant: "sertun", Single: BulkResult{MiBPerSecond: 3, Resources: Resources{TunnelCPUPercent: 300}}},
		{Scenario: "direct", Variant: "sertun-baseline", Single: BulkResult{MiBPerSecond: 2}},
	}
	summary := summarize(trials, []string{"direct"})
	if len(summary) != 2 || summary[0].Trials != 3 || summary[0].SingleThroughput != (Metric{Median: 3, Min: 1, Max: 100}) || summary[1].Variant != "sertun-baseline" {
		t.Fatalf("summary discarded an observation: %+v", summary)
	}
	if summary[0].SingleTunnelCost != (Metric{Median: 102.4, Min: 10.24, Max: 1024}) {
		t.Fatalf("CPU cost must normalize each trial before aggregation: %+v", summary[0].SingleTunnelCost)
	}
}

func TestEchoWorkload(t *testing.T) {
	server, err := startEcho()
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := shortConnections(ctx, server.listener.Addr().String(), 3)
	if err != nil || result.Count != 3 || len(result.LatenciesMS) != 3 || result.P95MS < result.P50MS {
		t.Fatalf("short workload = %+v, %v", result, err)
	}
}
