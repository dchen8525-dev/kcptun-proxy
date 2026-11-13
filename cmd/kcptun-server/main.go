package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"kcptun-proxy/internal/config"
	"kcptun-proxy/internal/logx"
	"kcptun-proxy/internal/stats"
	"kcptun-proxy/internal/tunnel"
)

func main() { os.Exit(run()) }

func run() int {
	cfg, err := config.Parse("server", os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	if cfg.Help {
		fmt.Print(config.Usage("server"))
		return 0
	}

	log, err := logx.New(cfg.LogLevel, cfg.LogFile, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer log.Close()

	tr, err := tunnel.NewTrace(cfg.Trace, "server")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer tr.Close()

	rep := stats.New(os.Stdout)
	rep.Start()
	defer rep.Stop()

	opts := tunnel.Options{
		Key:          cfg.Key,
		KCPMTU:       cfg.KCPMTU(),
		DataShards:   cfg.DataShards,
		ParityShards: cfg.ParityShards,
		Log:          log,
		Stats:        rep,
		Trace:        tr,
	}
	srv, err := tunnel.Listen(cfg.Bind, opts)
	if err != nil {
		log.Error("%v", err)
		return 1
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Info("signal: shutting down")
		_ = srv.Close()
	}()

	if err := srv.Serve(); err != nil {
		if ctx.Err() != nil {
			return 0
		}
		log.Error("serve: %v", err)
		return 1
	}
	return 0
}
