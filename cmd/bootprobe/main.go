package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if os.Getenv("GOBOOT_ORPHAN_CHILD") == "1" {
		time.Sleep(200 * time.Millisecond)
		fmt.Println("GOBOOT_ORPHAN_EXITED")
		return
	}
	if os.Getenv("GOBOOT_SPAWN_ORPHAN") == "1" {
		executable, err := os.Executable()
		if err != nil {
			fail("locate orphan executable", err)
		}
		child := exec.Command(executable)
		child.Env = append(os.Environ(), "GOBOOT_ORPHAN_CHILD=1", "GOBOOT_SPAWN_ORPHAN=")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fail("start orphan", err)
		}
		pid := child.Process.Pid
		if err := child.Process.Release(); err != nil {
			fail("release orphan", err)
		}
		if err := os.WriteFile("/tmp/goboot-orphan-pid", []byte(strconv.Itoa(pid)), 0600); err != nil {
			fail("write orphan PID", err)
		}
		fmt.Println("GOBOOT_ORPHAN_SPAWNED")
		return
	}
	if os.Getenv("GOBOOT_VERIFY_ORPHAN") == "1" {
		deadline := time.Now().Add(6 * time.Second)
		for {
			data, err := os.ReadFile("/tmp/goboot-orphan-pid")
			if err == nil {
				pid := strings.TrimSpace(string(data))
				if _, err := os.Stat("/proc/" + pid); os.IsNotExist(err) {
					fmt.Println("GOBOOT_ORPHAN_REAPED")
					break
				}
			}
			if time.Now().After(deadline) {
				fail("orphan was not reaped", err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if marker := os.Getenv("GOBOOT_FAIL_ONCE"); marker != "" {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			if err := os.WriteFile(marker, []byte("failed once\n"), 0600); err != nil {
				fail("create restart marker", err)
			}
			fmt.Println("GOBOOT_RESTART_FIRST_EXIT")
			os.Exit(1)
		} else if err != nil {
			fail("read restart marker", err)
		}
	}
	if marker := os.Getenv("GOBOOT_EXPECT_ROOT"); marker != "" {
		data, err := os.ReadFile(marker)
		if err != nil || string(data) != "goboot-final-root\n" {
			fail("final root marker", err)
		}
	}
	loopback, err := net.InterfaceByName("lo")
	if err != nil || loopback.Flags&net.FlagUp == 0 {
		fail("loopback link", err)
	}
	addresses, err := loopback.Addrs()
	if err != nil {
		fail("loopback addresses", err)
	}
	foundLoopback := false
	for _, address := range addresses {
		if address.String() == "127.0.0.1/8" {
			foundLoopback = true
		}
	}
	if !foundLoopback {
		fail("loopback 127.0.0.1/8", nil)
	}
	if name := os.Getenv("GOBOOT_EXPECT_NETWORK"); name != "" {
		iface, err := net.InterfaceByName(name)
		if err != nil || iface.Flags&net.FlagUp == 0 {
			fail("network link "+name, err)
		}
		addresses, err := iface.Addrs()
		if err != nil {
			fail("network addresses", err)
		}
		found := false
		foundV6 := false
		for _, address := range addresses {
			if address.String() == "192.0.2.10/24" {
				found = true
			}
			if address.String() == "2001:db8::10/64" {
				foundV6 = true
			}
		}
		if !found || !foundV6 {
			fail("network addresses", nil)
		}
		routes, err := os.ReadFile("/proc/net/route")
		if err != nil || !strings.Contains(string(routes), name+"\t00000000\t010200C0") {
			fail("network default route", err)
		}
	}
	if os.Getenv("GOBOOT_EXPECT_FILES") == "1" {
		for path, want := range map[string]string{
			"/etc/resolv.conf": "nameserver 192.0.2.1\n",
			"/etc/hosts":       "127.0.0.1 localhost\n",
		} {
			data, err := os.ReadFile(path)
			if err != nil || string(data) != want {
				fail("system file "+path, err)
			}
		}
	} else if os.Getenv("GOBOOT_EXPECT_FILES") == "absent" {
		for _, path := range []string{"/etc/resolv.conf", "/etc/hosts"} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				fail("absent system file "+path, err)
			}
		}
	}
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
	if os.Getenv("GOBOOT_TRIGGER_SHUTDOWN") == "1" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		defer signal.Stop(signals)
		fmt.Println("GOBOOT_SHUTDOWN_REQUESTED")
		if err := syscall.Kill(os.Getppid(), syscall.SIGTERM); err != nil {
			fail("request shutdown", err)
		}
		select {
		case <-signals:
			fmt.Println("GOBOOT_SERVICE_TERMINATED")
		case <-time.After(10 * time.Second):
			fail("shutdown signal timeout", nil)
		}
	}
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
