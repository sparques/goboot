package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	hostname, err := os.Hostname()
	if err != nil || hostname != "goboot-qemu" {
		fail("hostname", err)
	}
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		fail("mountinfo", err)
	}
	for target, fsType := range map[string]string{
		"/proc": "proc",
		"/dev":  "devtmpfs",
		"/sys":  "sysfs",
		"/tmp":  "tmpfs",
	} {
		if !mounted(info, target, fsType) {
			fail("mount "+target+" ("+fsType+")", nil)
		}
	}
	fmt.Println("GOBOOT_QEMU_OK: hostname, mounts, and service verified")
}

func mounted(info []byte, target, fsType string) bool {
	for _, line := range strings.Split(string(info), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		before := strings.Fields(parts[0])
		after := strings.Fields(parts[1])
		if len(before) >= 5 && len(after) >= 1 && before[4] == target && after[0] == fsType {
			return true
		}
	}
	return false
}

func fail(check string, err error) {
	fmt.Fprintf(os.Stderr, "GOBOOT_QEMU_FAIL: %s: %v\n", check, err)
	os.Exit(1)
}
