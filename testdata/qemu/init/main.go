package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sparques/goboot"
)

func main() {
	cfg, err := goboot.Load("/etc/goboot.yaml")
	if err != nil {
		fail(err)
	}
	runtime, err := goboot.Run(context.Background(), cfg)
	if err != nil {
		fail(err)
	}
	fmt.Println("GOBOOT_LIBRARY_RETURNED: imported init continues after Run")
	if err := runtime.Wait(); err != nil {
		fail(err)
	}
	if err := goboot.Reboot(goboot.PowerOff); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "GOBOOT_LIBRARY_FAIL:", err)
	for {
		time.Sleep(time.Hour)
	}
}
