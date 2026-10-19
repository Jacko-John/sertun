// Process management keeps benchmark children and their resource accounting
// separate from the load generator. Every started child is reaped on cleanup.

package bench

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	log  string
}

type processes struct{ tunnel, proxy []*process }

// startProcess captures diagnostics in the disposable trial directory.
func startProcess(dir, name, binary string, args ...string) (*process, error) {
	logPath := filepath.Join(dir, name+".log")
	log, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = log, log
	// A direct frp trial must not inherit an unrelated system proxy.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "NO_COLOR=1")
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{}), log: logPath}
	go func() {
		p.err = cmd.Wait()
		log.Close()
		close(p.done)
	}()
	return p, nil
}

// stop requests normal shutdown, bounds the wait, and always reaps the child.
func (p *process) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// close stops clients before servers, including partially started trials.
func (p *processes) close() {
	for _, group := range [][]*process{p.tunnel, p.proxy} {
		for i := len(group) - 1; i >= 0; i-- {
			group[i].stop()
		}
	}
}

// alive turns early process exits into useful local diagnostics, never results.
func (p *processes) alive() error {
	for _, group := range [][]*process{p.tunnel, p.proxy} {
		for _, child := range group {
			select {
			case <-child.done:
				data, _ := os.ReadFile(child.log)
				if len(data) > 4000 {
					data = data[len(data)-4000:]
				}
				return fmt.Errorf("%s exited (%v): %s", filepath.Base(child.cmd.Path), child.err, data)
			default:
			}
		}
	}
	return nil
}

// diagnostics is only emitted on a failed local run, outside published reports.
func (p *processes) diagnostics() string {
	var result strings.Builder
	for _, group := range [][]*process{p.tunnel, p.proxy} {
		for _, child := range group {
			data, _ := os.ReadFile(child.log)
			if len(data) > 3000 {
				data = data[len(data)-3000:]
			}
			fmt.Fprintf(&result, "%s: %s\n", filepath.Base(child.log), data)
		}
	}
	return result.String()
}

type usage struct{ tunnelRSS, proxyRSS, tunnelCPU, proxyCPU float64 }

// Resources reports summed process RSS and CPU, excluding the harness itself.
type Resources struct {
	TunnelRSSMiB     float64 `json:"tunnel_rss_mib"`
	ProxyRSSMiB      float64 `json:"proxy_rss_mib"`
	TunnelCPUPercent float64 `json:"tunnel_cpu_percent"`
	ProxyCPUPercent  float64 `json:"proxy_cpu_percent"`
}

// sample reads all PIDs in one ps invocation; RSS is KiB on supported systems.
func (p *processes) sample(ctx context.Context) (usage, error) {
	groups := make(map[int]bool)
	var ids []string
	for index, group := range [][]*process{p.tunnel, p.proxy} {
		for _, child := range group {
			pid := child.cmd.Process.Pid
			groups[pid] = index == 1
			ids = append(ids, strconv.Itoa(pid))
		}
	}
	output, err := exec.CommandContext(ctx, "ps", "-o", "pid=,rss=,time=", "-p", strings.Join(ids, ",")).Output()
	if err != nil {
		return usage{}, fmt.Errorf("sample processes: %w", err)
	}
	var result usage
	seen := 0
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return usage{}, fmt.Errorf("unexpected ps fields")
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return usage{}, err
		}
		rss, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return usage{}, err
		}
		cpu, err := parseCPUTime(fields[2])
		if err != nil {
			return usage{}, err
		}
		proxy, ok := groups[pid]
		if !ok {
			return usage{}, fmt.Errorf("unexpected ps PID")
		}
		if proxy {
			result.proxyRSS += rss / 1024
			result.proxyCPU += cpu
		} else {
			result.tunnelRSS += rss / 1024
			result.tunnelCPU += cpu
		}
		seen++
	}
	if seen != len(groups) {
		return usage{}, fmt.Errorf("a benchmark child exited during sampling")
	}
	return result, nil
}

// parseCPUTime accepts macOS mm:ss.xx and Linux [dd-]hh:mm:ss CPU totals.
func parseCPUTime(value string) (float64, error) {
	var days float64
	if day, rest, ok := strings.Cut(value, "-"); ok {
		n, err := strconv.ParseUint(day, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid ps CPU time %q", value)
		}
		days, value = float64(n), rest
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid ps CPU time %q", value)
	}
	var seconds float64
	for _, part := range parts {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid ps CPU time %q", value)
		}
		seconds = seconds*60 + n
	}
	return days*86400 + seconds, nil
}

// measure samples peak RSS during a phase and derives CPU from cumulative time.
func (p *processes) measure(ctx context.Context, run func() error) (Resources, error) {
	first, err := p.sample(ctx)
	if err != nil {
		return Resources{}, err
	}
	start := time.Now()
	stop := make(chan struct{})
	type sampled struct {
		peak usage
		err  error
	}
	done := make(chan sampled, 1)
	go func() {
		peak := first
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				done <- sampled{peak: peak}
				return
			case <-ticker.C:
				next, err := p.sample(ctx)
				if err != nil {
					done <- sampled{err: err}
					return
				}
				peak.tunnelRSS = max(peak.tunnelRSS, next.tunnelRSS)
				peak.proxyRSS = max(peak.proxyRSS, next.proxyRSS)
			}
		}
	}()
	runErr := run()
	elapsed := time.Since(start).Seconds()
	close(stop)
	samples := <-done
	last, err := p.sample(ctx)
	if runErr != nil {
		return Resources{}, runErr
	}
	if samples.err != nil {
		return Resources{}, samples.err
	}
	if err != nil {
		return Resources{}, err
	}
	return Resources{
		TunnelRSSMiB: max(samples.peak.tunnelRSS, last.tunnelRSS), ProxyRSSMiB: max(samples.peak.proxyRSS, last.proxyRSS),
		TunnelCPUPercent: (last.tunnelCPU - first.tunnelCPU) / elapsed * 100,
		ProxyCPUPercent:  (last.proxyCPU - first.proxyCPU) / elapsed * 100,
	}, nil
}
