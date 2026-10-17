// Command ops-agent runs on each monitored VPS and reports to ops-server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"fleetwatch/internal/agent/config"
	"fleetwatch/internal/agent/conn"
	"fleetwatch/internal/agent/inventory"
	"fleetwatch/internal/agent/sites"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: ops-agent <command>

commands:
  run                                    connect to the server (default; used by ops-agent.service)
  enroll --server URL --token TOKEN      pair this host using a one-time token from the dashboard
  inventory [--roots /var/www,/srv]      print what the agent discovers, as JSON
  install-caddy                          install Caddy (for sites) if missing, open ports 80/443
  version                                print the version

Config: /etc/ops-agent/config.json (override with OPS_AGENT_CONFIG).
`

func main() {
	cmd := "run"
	args := []string{}
	if len(os.Args) > 1 {
		cmd, args = os.Args[1], os.Args[2:]
	}
	path := config.DefaultPath
	if p := os.Getenv("OPS_AGENT_CONFIG"); p != "" {
		path = p
	}
	var err error
	switch cmd {
	case "run":
		err = run(path)
	case "enroll":
		err = enroll(path, args)
	case "inventory":
		err = printInventory(args)
	case "install-caddy":
		if err = sites.InstallCaddy(context.Background(), os.Stdout); err == nil {
			sites.OpenWebPorts(context.Background(), os.Stdout)
		}
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if errors.Is(err, conn.ErrRestart) {
		os.Exit(0) // systemd (Restart=always) starts us again
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if cfg.ServerURL, err = config.CheckServerURL(cfg.ServerURL); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("ops-agent starting", "version", version, "server", cfg.ServerURL, "server_id", cfg.ServerID)
	state := "/var/lib/ops-agent/state.json"
	if p := os.Getenv("OPS_AGENT_STATE"); p != "" {
		state = p
	}
	return (&conn.Runner{Cfg: cfg, Version: version, StatePath: state}).Run(ctx)
}

func enroll(path string, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	server := fs.String("server", "", "ops-server URL, e.g. https://ops.example.com")
	token := fs.String("token", "", "one-time enrollment token from the dashboard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *token == "" {
		return errors.New("enroll needs --server and --token")
	}
	url, err := config.CheckServerURL(*server)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := conn.Enroll(ctx, url, *token)
	if err != nil {
		return err
	}
	cfg := config.Config{AllowCustomCommands: true}
	if old, err := config.Load(path); err == nil {
		cfg.AllowCustomCommands = old.AllowCustomCommands // keep the local safety switch on re-pair
	}
	cfg.ServerURL, cfg.ServerID, cfg.Credential = url, res.ServerID, res.Credential
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Printf("Enrolled as server %s. Credential saved to %s.\n", res.ServerID, path)
	return nil
}

func printInventory(args []string) error {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	roots := fs.String("roots", "/var/www", "comma-separated project directories to scan")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := inventory.NewCollector()
	c.Collect(context.Background(), nil) // first pass primes CPU counters
	time.Sleep(time.Second)
	inv := c.Collect(context.Background(), strings.Split(*roots, ","))
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(inv)
}
