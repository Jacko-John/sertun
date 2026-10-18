// The runner builds the production executable, starts isolated topologies, and
// rotates comparison order. It never reads the user's proxy configuration.
package bench

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Options defines a common workload for all compared implementations.
type Options struct {
	Scenarios   []string      `json:"scenarios"`
	Trials      int           `json:"trials"`
	Duration    time.Duration `json:"duration_ns"`
	ShortCount  int           `json:"short_count"`
	Parallel    int           `json:"parallel"`
	BaselineRef string        `json:"baseline_ref,omitempty"`
}

// Trial preserves every successful run; any failed phase aborts publication.
type Trial struct {
	Scenario string      `json:"scenario"`
	Variant  string      `json:"variant"`
	Round    int         `json:"round"`
	Idle     Resources   `json:"idle"`
	Short    ShortResult `json:"short"`
	Single   BulkResult  `json:"single"`
	Parallel BulkResult  `json:"parallel"`
}

// Validate rejects expensive accidental workloads and unknown scenario names.
func (o Options) Validate() error {
	if o.Trials < 1 || o.Trials > 20 || o.Duration < 200*time.Millisecond || o.Duration > time.Minute || o.ShortCount < 1 || o.ShortCount > 10000 || o.Parallel < 2 || o.Parallel > 32 {
		return fmt.Errorf("require trials 1..20, duration 200ms..1m, short-count 1..10000, parallel 2..32")
	}
	if len(o.Scenarios) == 0 {
		return fmt.Errorf("at least one scenario is required")
	}
	seen := make(map[string]bool)
	for _, name := range o.Scenarios {
		if name != "direct" && name != "mihomo-direct" && name != "mihomo-anytls" && name != "mihomo-hy2" {
			return fmt.Errorf("unknown scenario %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate scenario %q", name)
		}
		seen[name] = true
	}
	return nil
}

// Run returns a complete report only after all requested trials succeed.
func Run(ctx context.Context, options Options, progress io.Writer) (*Report, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	fmt.Fprintln(progress, "Preparing pinned frp and Mihomo tools (verified cache / official releases)...")
	downloadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	binaries, err := prepareTools(downloadCtx)
	cancel()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "sertun-bench-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	binaries.sertun = filepath.Join(dir, "sertun")
	commit, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return nil, fmt.Errorf("run from the sertun repository root: %w", err)
	}
	version := strings.TrimSpace(string(commit))
	fmt.Fprintln(progress, "Building sertun with CGO_ENABLED=0, -trimpath, -s -w...")
	if err := buildSertun(ctx, ".", binaries.sertun, version); err != nil {
		return nil, err
	}
	if options.BaselineRef != "" {
		fmt.Fprintln(progress, "Building baseline Git revision with the same Go compiler...")
		binaries.baseline, options.BaselineRef, err = buildBaseline(ctx, dir, options.BaselineRef)
		if err != nil {
			return nil, err
		}
	}
	report, err := newReport(ctx, options, version, binaries)
	if err != nil {
		return nil, err
	}
	backend, err := startEcho()
	if err != nil {
		return nil, err
	}
	defer backend.close()
	variants := []string{"sertun", "frp-plain", "frp-mux"}
	if binaries.baseline != "" {
		variants = []string{"sertun", "sertun-baseline", "frp-plain", "frp-mux"}
	}
	for round := 1; round <= options.Trials; round++ {
		// Rotate orders; N rounds give each of the N variants every position.
		for offset := range len(options.Scenarios) {
			scenario := options.Scenarios[(offset+round-1)%len(options.Scenarios)]
			for index := range len(variants) {
				variant := variants[(index+round-1)%len(variants)]
				fmt.Fprintf(progress, "Round %d/%d: %-14s %-9s ... ", round, options.Trials, scenario, variant)
				trial, err := runTrial(ctx, dir, backend.listener.Addr().String(), scenario, variant, round, options, binaries)
				if err != nil {
					return nil, fmt.Errorf("round %d %s %s: %w", round, scenario, variant, err)
				}
				report.Trials = append(report.Trials, trial)
				fmt.Fprintf(progress, "RSS %.1f MiB; single %.1f / parallel %.1f MiB/s; p95 %.2f ms\n", trial.Idle.TunnelRSSMiB, trial.Single.MiBPerSecond, trial.Parallel.MiBPerSecond, trial.Short.P95MS)
			}
		}
	}
	report.FinishedAt = time.Now().UTC()
	report.Summary = summarize(report.Trials, options.Scenarios)
	return report, nil
}

// runTrial owns its temporary directory and all children, even during startup.
func runTrial(ctx context.Context, root, backend, scenario, variant string, round int, options Options, binaries tools) (trial Trial, err error) {
	trial = Trial{Scenario: scenario, Variant: variant, Round: round}
	dir, err := os.MkdirTemp(root, "trial-")
	if err != nil {
		return trial, err
	}
	defer os.RemoveAll(dir)
	children := &processes{}
	defer children.close()
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w\n%s", err, children.diagnostics())
		}
	}()
	ports, err := freePorts(4)
	if err != nil {
		return trial, err
	}
	proxy, err := startProxy(ctx, dir, scenario, ports[2], ports[3], binaries, children)
	if err != nil {
		return trial, err
	}
	if err := startTunnel(ctx, dir, variant, ports[0], ports[1], backend, proxy, binaries, children); err != nil {
		return trial, err
	}
	address := fmt.Sprintf("127.0.0.1:%d", ports[1])
	if err := waitReady(ctx, address, children); err != nil {
		return trial, err
	}
	if _, err := shortConnections(ctx, address, 10); err != nil {
		return trial, err
	}
	// A fixed quiet period separates startup/GC from the idle phase.
	if err := pause(ctx, time.Second); err != nil {
		return trial, err
	}
	trial.Idle, err = children.measure(ctx, func() error { return pause(ctx, time.Second) })
	if err != nil {
		return trial, err
	}
	trial.Short, err = shortConnections(ctx, address, options.ShortCount)
	if err != nil {
		return trial, fmt.Errorf("short connections: %w", err)
	}
	trial.Single, err = bulk(ctx, address, 1, options.Duration, children)
	if err != nil {
		return trial, fmt.Errorf("single stream: %w", err)
	}
	trial.Parallel, err = bulk(ctx, address, options.Parallel, options.Duration, children)
	if err != nil {
		return trial, fmt.Errorf("parallel streams: %w", err)
	}
	return trial, children.alive()
}

// freePorts holds all reservations together to avoid selecting duplicate ports.
// External programs cannot inherit them; a rare bind race fails the whole run.
func freePorts(count int) ([]int, error) {
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	var ports []int
	for range count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, listener)
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

// pause observes cancellation during the deliberately quiet measurement phases.
func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// waitPort checks listener readiness without imposing an arbitrary startup delay.
func waitPort(ctx context.Context, address string, children *processes) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if err := children.alive(); err != nil {
			return err
		}
		conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", address)
		if err == nil {
			conn.Close()
			return nil
		}
		if err := pause(ctx, 20*time.Millisecond); err != nil {
			return fmt.Errorf("listener readiness: %w", err)
		}
	}
}

// platformInfo adds hardware details using only built-in OS commands.
func platformInfo(ctx context.Context) (cpu, memory, system string) {
	if runtime.GOOS == "darwin" {
		for name, target := range map[string]*string{"machdep.cpu.brand_string": &cpu, "hw.memsize": &memory, "kern.osproductversion": &system} {
			data, _ := exec.CommandContext(ctx, "sysctl", "-n", name).Output()
			*target = strings.TrimSpace(string(data))
		}
	} else {
		data, _ := os.ReadFile("/proc/cpuinfo")
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Hardware") {
				_, cpu, _ = strings.Cut(line, ":")
				cpu = strings.TrimSpace(cpu)
				break
			}
		}
		data, _ = os.ReadFile("/proc/meminfo")
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				memory = strings.TrimSpace(strings.TrimPrefix(line, "MemTotal:"))
				break
			}
		}
		data, _ = exec.CommandContext(ctx, "uname", "-r").Output()
		system = strings.TrimSpace(string(data))
	}
	return
}
