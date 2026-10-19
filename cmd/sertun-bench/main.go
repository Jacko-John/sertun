// Command sertun-bench runs the opt-in local comparison harness. It builds the
// production sertun binary, starts isolated tunnel and proxy processes, and
// refuses to overwrite an existing result file.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sertun/internal/bench"
)

// main parses the workload, runs it, and saves both report formats.
func main() {
	options := bench.Options{}
	scenarios := flag.String("scenarios", "direct,mihomo-direct,mihomo-anytls,mihomo-hy2", "comma-separated local scenarios")
	flag.IntVar(&options.Trials, "trials", 3, "fresh trials per configuration")
	flag.DurationVar(&options.Duration, "duration", 2*time.Second, "duration per bulk phase")
	flag.IntVar(&options.ShortCount, "short-count", 100, "sequential short connections per trial")
	flag.IntVar(&options.Parallel, "parallel", 8, "streams in the parallel phase")
	flag.StringVar(&options.BaselineRef, "baseline-ref", "", "optional Git revision to rebuild and compare with the current sertun")
	output := flag.String("output", "bench/results/local.json", "new JSON output path; also writes Markdown")
	flag.Parse()
	options.Scenarios = strings.Split(*scenarios, ",")
	if err := bench.CheckOutput(*output); err != nil {
		fmt.Fprintln(os.Stderr, "sertun-bench:", err)
		os.Exit(1)
	}
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "sertun-bench: unexpected positional arguments")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, err := bench.Run(ctx, options, os.Stderr)
	if err == nil {
		err = report.Save(*output)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sertun-bench:", err)
		os.Exit(1)
	}
	fmt.Println("Saved", *output, "and its Markdown report.")
}
