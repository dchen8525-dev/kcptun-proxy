package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"kcptun-proxy/internal/config"
	"kcptun-proxy/internal/logx"
	"kcptun-proxy/internal/proxy"
	"kcptun-proxy/internal/stats"
	"kcptun-proxy/internal/tunnel"
)

func main() { os.Exit(run()) }

func run() int {
	cfg, err := config.Parse("client", os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	if cfg.Help {
		fmt.Print(config.Usage("client"))
		return 0
	}

	opts := tunnel.Options{
		Key:          cfg.Key,
		KCPMTU:       cfg.KCPMTU(),
		DataShards:   cfg.DataShards,
		ParityShards: cfg.ParityShards,
	}

	if cfg.Speedtest {
		err := tunnel.Speedtest(cfg.Seconds, opts, func(s string) { fmt.Println(s) })
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		return 0
	}

	log, err := logx.New(cfg.LogLevel, cfg.LogFile, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer log.Close()

	tr, err := tunnel.NewTrace(cfg.Trace, "client")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer tr.Close()

	rep := stats.New(os.Stdout)
	rep.Start()
	defer rep.Stop()
	opts.Log, opts.Stats, opts.Trace = log, rep, tr

	cl := tunnel.NewClient(cfg.Peer, opts)
	defer cl.Close()

	ln, err := proxy.Listen(cfg.Bind, cl, rep, log, tr)
	if err != nil {
		log.Error("local listen %s: %v", cfg.Bind, err)
		return 1
	}
	defer ln.Close()
	log.Info("client: proxy %s -> KCP %s (fec=%d:%d mtu=%d)",
		ln.Addr(), cfg.Peer, cfg.DataShards, cfg.ParityShards, cfg.MTU)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Info("signal: shutting down")
		_ = ln.Close()
	}()

	if err := ln.Serve(); err != nil {
		if ctx.Err() != nil {
			return 0
		}
		log.Error("serve: %v", err)
		return 1
	}
	return 0
}
