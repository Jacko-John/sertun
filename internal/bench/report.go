// Reports preserve raw observations and derive all tables from those same
// observations. No successful sample is selected or discarded by its score.
package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Environment omits hostnames and absolute paths from shareable results.
type Environment struct {
	OS            string `json:"os"`
	SystemVersion string `json:"system_version"`
	Arch          string `json:"arch"`
	CPU           string `json:"cpu"`
	LogicalCPUs   int    `json:"logical_cpus"`
	Memory        string `json:"memory"`
	HarnessGo     string `json:"harness_go"`
	GitCommit     string `json:"git_commit"`
	WorktreeDirty bool   `json:"worktree_dirty"`
	ProductDirty  bool   `json:"product_source_dirty"`
}

// Report is the versioned, self-contained input to the generated Markdown.
type Report struct {
	Schema      int         `json:"schema"`
	StartedAt   time.Time   `json:"started_at"`
	FinishedAt  time.Time   `json:"finished_at"`
	Environment Environment `json:"environment"`
	Tools       []ToolInfo  `json:"tools"`
	Options     Options     `json:"options"`
	Trials      []Trial     `json:"trials"`
	Summary     []Summary   `json:"summary"`
}

// Metric preserves spread alongside the arithmetic median of the trials.
type Metric struct {
	Median float64 `json:"median"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// Summary contains the primary comparison metrics; phase RSS remains in Trials.
type Summary struct {
	Scenario           string `json:"scenario"`
	Variant            string `json:"variant"`
	Trials             int    `json:"trials"`
	IdleTunnelRSS      Metric `json:"idle_tunnel_rss_mib"`
	IdleProxyRSS       Metric `json:"idle_proxy_rss_mib"`
	ShortP50           Metric `json:"short_p50_ms"`
	ShortP95           Metric `json:"short_p95_ms"`
	SingleThroughput   Metric `json:"single_mib_per_second"`
	ParallelThroughput Metric `json:"parallel_mib_per_second"`
	SingleTunnelCPU    Metric `json:"single_tunnel_cpu_percent"`
	SingleProxyCPU     Metric `json:"single_proxy_cpu_percent"`
	ParallelTunnelCPU  Metric `json:"parallel_tunnel_cpu_percent"`
	ParallelProxyCPU   Metric `json:"parallel_proxy_cpu_percent"`
}

// newReport captures actual executable hashes and compiler versions before load.
func newReport(ctx context.Context, options Options, commit string, binaries tools) (*Report, error) {
	cpu, memory, system := platformInfo(ctx)
	status, err := exec.CommandContext(ctx, "git", "status", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	productStatus, err := exec.CommandContext(ctx, "git", "status", "--porcelain", "--", "cmd/sertun", "internal/client", "internal/server", "internal/config", "internal/dial", "internal/relay", "internal/wire", "go.mod", "go.sum").Output()
	if err != nil {
		return nil, err
	}
	report := &Report{
		Schema: 1, StartedAt: time.Now().UTC(), Options: options,
		Environment: Environment{
			OS: runtime.GOOS, SystemVersion: system, Arch: runtime.GOARCH, CPU: cpu,
			Memory: memory, LogicalCPUs: runtime.NumCPU(), HarnessGo: runtime.Version(),
			GitCommit: commit, WorktreeDirty: len(status) != 0, ProductDirty: len(productStatus) != 0,
		},
	}
	for _, tool := range []struct{ name, version, path string }{{"sertun", commit, binaries.sertun}, {"frpc", frpVersion, binaries.frpc}, {"frps", frpVersion, binaries.frps}, {"mihomo", mihomoVersion, binaries.mihomo}} {
		info, err := describeTool(tool.name, tool.version, tool.path)
		if err != nil {
			return nil, err
		}
		report.Tools = append(report.Tools, info)
	}
	return report, nil
}

// metric computes a median without mutating the raw observations.
func metric(values []float64) Metric {
	values = slices.Clone(values)
	slices.Sort(values)
	middle := len(values) / 2
	median := values[middle]
	if len(values)%2 == 0 {
		median = (values[middle-1] + median) / 2
	}
	return Metric{Median: median, Min: values[0], Max: values[len(values)-1]}
}

// summarize groups every trial and keeps a stable human-readable table order.
func summarize(trials []Trial, scenarios []string) []Summary {
	var summaries []Summary
	for _, scenario := range scenarios {
		for _, variant := range []string{"sertun", "frp-plain", "frp-mux"} {
			var group []Trial
			for _, trial := range trials {
				if trial.Scenario == scenario && trial.Variant == variant {
					group = append(group, trial)
				}
			}
			if len(group) == 0 {
				continue
			}
			collect := func(value func(Trial) float64) Metric {
				var values []float64
				for _, trial := range group {
					values = append(values, value(trial))
				}
				return metric(values)
			}
			summaries = append(summaries, Summary{
				Scenario: scenario, Variant: variant, Trials: len(group),
				IdleTunnelRSS:      collect(func(t Trial) float64 { return t.Idle.TunnelRSSMiB }),
				IdleProxyRSS:       collect(func(t Trial) float64 { return t.Idle.ProxyRSSMiB }),
				ShortP50:           collect(func(t Trial) float64 { return t.Short.P50MS }),
				ShortP95:           collect(func(t Trial) float64 { return t.Short.P95MS }),
				SingleThroughput:   collect(func(t Trial) float64 { return t.Single.MiBPerSecond }),
				ParallelThroughput: collect(func(t Trial) float64 { return t.Parallel.MiBPerSecond }),
				SingleTunnelCPU:    collect(func(t Trial) float64 { return t.Single.Resources.TunnelCPUPercent }),
				SingleProxyCPU:     collect(func(t Trial) float64 { return t.Single.Resources.ProxyCPUPercent }),
				ParallelTunnelCPU:  collect(func(t Trial) float64 { return t.Parallel.Resources.TunnelCPUPercent }),
				ParallelProxyCPU:   collect(func(t Trial) float64 { return t.Parallel.Resources.ProxyCPUPercent }),
			})
		}
	}
	return summaries
}

// CheckOutput validates names before an expensive run and protects old reports.
func CheckOutput(path string) error {
	if filepath.Ext(path) != ".json" {
		return fmt.Errorf("output must end in .json")
	}
	markdown := strings.TrimSuffix(path, ".json") + ".md"
	for _, name := range []string{path, markdown} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			return fmt.Errorf("output already exists or cannot be checked: %s", name)
		}
	}
	return nil
}

// Save refuses to overwrite previous evidence, including concurrent writers.
func (r *Report) Save(path string) error {
	if err := CheckOutput(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := writeNewFile(path, append(data, '\n')); err != nil {
		return err
	}
	return writeNewFile(strings.TrimSuffix(path, ".json")+".md", []byte(r.Markdown()))
}

// writeNewFile creates one report without replacing an existing result.
func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// Markdown derives readable medians and ranges directly from the JSON summary.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprint(&b, "# Local process benchmark\n\n")
	e := r.Environment
	fmt.Fprintf(&b, "Started: %s. Finished: %s.\n\n", r.StartedAt.Format(time.RFC3339), r.FinishedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "Environment: %s %s / %s; %s; %d logical CPUs; memory %s; harness %s.\n\n", e.OS, e.SystemVersion, e.Arch, e.CPU, e.LogicalCPUs, e.Memory, e.HarnessGo)
	fmt.Fprintf(&b, "Source: `%s`; worktree dirty: %t; product source dirty: %t. sertun build: `CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=<commit>'`.\n\n", e.GitCommit, e.WorktreeDirty, e.ProductDirty)
	fmt.Fprintf(&b, "Workload: %d trials/configuration; %s per bulk phase; %d short requests/trial (64-byte echo); %d parallel streams; 10 warmup requests; 1s settle + 1s idle observation. All endpoints use loopback.\n\n", r.Options.Trials, r.Options.Duration, r.Options.ShortCount, r.Options.Parallel)
	fmt.Fprintln(&b, "| Tool | Version | Build Go | Binary MiB | SHA-256 |\n| --- | --- | --- | ---: | --- |")
	for _, tool := range r.Tools {
		fmt.Fprintf(&b, "| %s | %s | %s | %.2f | `%s` |\n", tool.Name, tool.Version, tool.GoVersion, float64(tool.Bytes)/(1<<20), tool.SHA256)
	}
	fmt.Fprintln(&b, "\nValues: median [minimum, maximum] across trials. Throughput counts received echo payload once; the payload also traverses the opposite direction. RSS sums client + server and is sampled at 200ms intervals. CPU 100% = one logical core; ps CPU time resolution is platform-dependent (macOS 0.01s, Linux typically 1s).")
	fmt.Fprintln(&b, "\n| Scenario | Variant | Tunnel idle MiB | Outer idle MiB | Short p50 ms | Short p95 ms | Single MiB/s | Parallel MiB/s |\n| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |")
	format := func(m Metric) string { return fmt.Sprintf("%.2f [%.2f, %.2f]", m.Median, m.Min, m.Max) }
	for _, s := range r.Summary {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", s.Scenario, s.Variant, format(s.IdleTunnelRSS), format(s.IdleProxyRSS), format(s.ShortP50), format(s.ShortP95), format(s.SingleThroughput), format(s.ParallelThroughput))
	}
	fmt.Fprintln(&b, "\n| Scenario | Variant | Single tunnel CPU % | Single outer CPU % | Parallel tunnel CPU % | Parallel outer CPU % |\n| --- | --- | ---: | ---: | ---: | ---: |")
	for _, s := range r.Summary {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", s.Scenario, s.Variant, format(s.SingleTunnelCPU), format(s.SingleProxyCPU), format(s.ParallelTunnelCPU), format(s.ParallelProxyCPU))
	}
	fmt.Fprintln(&b, "\nfrp-plain: TCP mux/TLS/payload encryption/compression disabled, poolCount=0. frp-mux: same settings with TCP mux enabled. AnyTLS/HY2 use real local Mihomo endpoints and an ephemeral self-signed certificate; only certificate trust verification is skipped. No WAN loss, RTT, reconnect or outer half-close claims follow from these workloads. Official releases may use different Go compilers, as recorded above.")
	return b.String()
}
