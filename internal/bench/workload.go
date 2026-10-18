// Workloads measure complete reverse connections and validated TCP echo traffic.
// Setup happens outside bulk timing; each phase owns and closes its sockets.
package bench

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"sort"
	"sync"
	"time"
)

const payloadByte = 0x5a

type echoServer struct {
	listener net.Listener
	mu       sync.Mutex
	active   map[net.Conn]struct{}
	done     sync.WaitGroup
	accepted chan struct{}
}

// startEcho starts a dynamic loopback backend shared by every comparison.
func startEcho() (*echoServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &echoServer{listener: listener, active: make(map[net.Conn]struct{}), accepted: make(chan struct{})}
	s.done.Go(func() {
		defer close(s.accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.active[conn] = struct{}{}
			s.mu.Unlock()
			s.done.Go(func() {
				_, _ = io.Copy(conn, conn)
				conn.Close()
				s.mu.Lock()
				delete(s.active, conn)
				s.mu.Unlock()
			})
		}
	})
	return s, nil
}

// close first joins Accept, then closes all handlers before waiting for them.
func (s *echoServer) close() {
	s.listener.Close()
	<-s.accepted
	s.mu.Lock()
	for conn := range s.active {
		conn.Close()
	}
	s.mu.Unlock()
	s.done.Wait()
}

// echoOnce measures Dial through the first full response, not just TCP accept.
func echoOnce(ctx context.Context, address string) (time.Duration, error) {
	start := time.Now()
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	_ = conn.SetDeadline(deadline)
	payload := bytes.Repeat([]byte{payloadByte}, 64)
	if _, err := io.Copy(conn, bytes.NewReader(payload)); err != nil {
		return 0, err
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return 0, err
	}
	if !bytes.Equal(payload, got) {
		return 0, fmt.Errorf("echo response mismatch")
	}
	return time.Since(start), nil
}

// waitReady retries a full echo with bounded attempts and checks child exits.
func waitReady(ctx context.Context, address string, children *processes) error {
	readyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var last error
	for {
		if err := children.alive(); err != nil {
			return err
		}
		if _, last = echoOnce(readyCtx, address); last == nil {
			return nil
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("echo readiness: %w (last attempt: %v)", readyCtx.Err(), last)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ShortResult retains every request latency for percentile auditing.
type ShortResult struct {
	Count       int       `json:"count"`
	P50MS       float64   `json:"p50_ms"`
	P95MS       float64   `json:"p95_ms"`
	LatenciesMS []float64 `json:"latencies_ms"`
}

// shortConnections records sequential full setup/response latencies in order.
func shortConnections(ctx context.Context, address string, count int) (ShortResult, error) {
	result := ShortResult{Count: count, LatenciesMS: make([]float64, 0, count)}
	for range count {
		elapsed, err := echoOnce(ctx, address)
		if err != nil {
			return result, err
		}
		result.LatenciesMS = append(result.LatenciesMS, float64(elapsed)/float64(time.Millisecond))
	}
	result.P50MS = percentile(result.LatenciesMS, 0.50)
	result.P95MS = percentile(result.LatenciesMS, 0.95)
	return result, nil
}

// percentile uses the nearest-rank definition on a copy of the observations.
func percentile(values []float64, fraction float64) float64 {
	values = append([]float64(nil), values...)
	sort.Float64s(values)
	index := int(math.Ceil(float64(len(values))*fraction)) - 1
	return values[max(0, min(index, len(values)-1))]
}

// BulkResult counts received payload once, even though echo traverses both ways.
type BulkResult struct {
	Connections     int       `json:"connections"`
	DurationSeconds float64   `json:"duration_seconds"`
	ReceivedBytes   int64     `json:"received_bytes"`
	MiBPerSecond    float64   `json:"mib_per_second"`
	Resources       Resources `json:"resources"`
}

// cpuCost normalizes process CPU by completed payload; 100% is one CPU second
// per wall second. This compares efficiency even when throughput differs.
func (r BulkResult) cpuCost(includeProxy bool) float64 {
	if r.MiBPerSecond == 0 {
		return 0
	}
	percent := r.Resources.TunnelCPUPercent
	if includeProxy {
		percent += r.Resources.ProxyCPUPercent
	}
	return percent / 100 / (r.MiBPerSecond / 1024)
}

// bulk opens connections before timing and validates every received byte. A
// common deadline ends readers and writers without depending on half-close.
func bulk(ctx context.Context, address string, count int, duration time.Duration, children *processes) (BulkResult, error) {
	var conns []net.Conn
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()
	for range count {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return BulkResult{}, err
		}
		conns = append(conns, conn)
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte{payloadByte}); err != nil {
			return BulkResult{}, err
		}
		if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
			return BulkResult{}, err
		}
	}
	result := BulkResult{Connections: count}
	resources, err := children.measure(ctx, func() error {
		start := time.Now()
		end := start.Add(duration)
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(end) {
			return fmt.Errorf("context deadline would truncate the bulk phase")
		}
		stopCancel := context.AfterFunc(ctx, func() {
			for _, conn := range conns {
				conn.Close()
			}
		})
		defer stopCancel()
		type completion struct {
			bytes int64
			err   error
		}
		done := make(chan completion, count*2)
		for _, conn := range conns {
			_ = conn.SetDeadline(end)
			go func() {
				payload := bytes.Repeat([]byte{payloadByte}, 64<<10)
				for {
					if _, err := conn.Write(payload); err != nil {
						done <- completion{err: phaseError(err, end)}
						return
					}
				}
			}()
			go func() {
				buffer := make([]byte, 64<<10)
				want := bytes.Repeat([]byte{payloadByte}, len(buffer))
				var received int64
				for {
					n, err := conn.Read(buffer)
					if !bytes.Equal(buffer[:n], want[:n]) {
						done <- completion{err: fmt.Errorf("bulk echo mismatch")}
						return
					}
					received += int64(n)
					if err != nil {
						done <- completion{bytes: received, err: phaseError(err, end)}
						return
					}
				}
			}()
		}
		var firstErr error
		for range count * 2 {
			completion := <-done
			result.ReceivedBytes += completion.bytes
			if completion.err != nil && firstErr == nil {
				firstErr = completion.err
				for _, conn := range conns {
					conn.Close()
				}
			}
		}
		result.DurationSeconds = time.Since(start).Seconds()
		return firstErr
	})
	result.Resources = resources
	if err != nil {
		return result, err
	}
	if result.ReceivedBytes == 0 {
		return result, fmt.Errorf("no bulk echo payload received")
	}
	result.MiBPerSecond = float64(result.ReceivedBytes) / (1 << 20) / result.DurationSeconds
	return result, nil
}

// phaseError treats only the scheduled socket timeout as a successful stop.
func phaseError(err error, end time.Time) error {
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() && !time.Now().Before(end) {
		return nil
	}
	return err
}
