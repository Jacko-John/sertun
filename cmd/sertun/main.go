// Command sertun runs the mapping server or the reverse client. Configuration
// is loaded and validated before any port is opened.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"sertun/internal/client"
	"sertun/internal/config"
	"sertun/internal/server"
	"sertun/internal/wire"
)

var version = "dev"

// main prints one error and exits non-zero unless the command succeeded.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "sertun:", err)
		os.Exit(1)
	}
}

// run dispatches one command line. Output streams are explicit so the
// command stays testable.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return fmt.Errorf("expected a command")
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return nil
	case "version", "--version":
		fmt.Fprintf(stdout, "sertun %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	case "token":
		token, err := wire.NewID()
		if err == nil {
			fmt.Fprintln(stdout, token)
		}
		return err
	case "server", "client":
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("c", args[0]+".toml", "TOML configuration file")
	check := flags.Bool("check", false, "validate configuration and exit without opening ports")
	debug := flags.Bool("debug", false, "enable debug logging")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	if args[0] == "server" {
		cfg, err := config.LoadServer(*path)
		if err != nil {
			return err
		}
		if *check {
			fmt.Fprintln(stdout, "server configuration is valid")
			return nil
		}
		srv, err := server.New(cfg, logger)
		if err != nil {
			return err
		}
		return srv.Run(ctx)
	}
	cfg, err := config.LoadClient(*path)
	if err != nil {
		return err
	}
	if *check {
		fmt.Fprintln(stdout, "client configuration is valid")
		return nil
	}
	cli, err := client.New(cfg, logger)
	if err != nil {
		return err
	}
	return cli.Run(ctx)
}

// usage writes the command summary.
func usage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  sertun server [-c server.toml] [-check] [-debug]
  sertun client [-c client.toml] [-check] [-debug]
  sertun token
  sertun version

TCP reverse port mapping over direct connections or an HTTP CONNECT proxy.
Transport encryption is provided by the independently running outer proxy.`)
}
