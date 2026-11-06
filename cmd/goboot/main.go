package main

import (
	"bytes"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sparques/goboot"
)

//go:embed embedded.yaml
var embeddedConfig []byte

func main() {
	configPath := flag.String("config", "", "YAML config path (overrides embedded config)")
	check := flag.Bool("check", false, "validate config and exit")
	flag.Parse()
	if *configPath == "" && len(bytes.TrimSpace(embeddedConfig)) == 0 {
		*configPath = "/etc/goboot.yaml"
	}

	cfg, err := goboot.LoadSelected(*configPath, embeddedConfig)
	if err != nil {
		startupError("configuration failed", err)
		fail()
	}
	if *check {
		if os.Getpid() == 1 {
			startupError("check mode cannot run as PID 1", nil)
			fail()
		}
		fmt.Println("configuration valid")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime, err := goboot.Run(ctx, cfg)
	if err != nil {
		startupError("boot failed", err)
		fail()
	}
	if err := runtime.Wait(); err != nil {
		startupError("service failed", err)
		fail()
	}
	if os.Getpid() == 1 {
		switch cfg.OnExit {
		case "reboot":
			err = goboot.Reboot(goboot.Restart)
		case "poweroff":
			err = goboot.Reboot(goboot.PowerOff)
		default:
			err = goboot.WaitForChildren(ctx)
		}
		startupError("PID 1 exit action failed", err)
		fail()
	}
}

func startupError(message string, err error) {
	if os.Getpid() == 1 {
		logger, closer, openErr := goboot.OpenLogger("")
		if openErr == nil {
			logger.Error(message, "error", err)
			closer.Close()
			return
		}
	}
	slog.Error(message, "error", err)
}

func fail() {
	if os.Getpid() == 1 {
		// Until the PID 1 supervisor exists, stay alive after an early error.
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(1)
}
