package goboot

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSetupNetworkOrder(t *testing.T) {
	var events []string
	interfaces := []InterfaceConfig{{Name: "eth0", Addresses: []string{"192.0.2.10/24", "2001:db8::10/64"}, Routes: []RouteConfig{{Destination: "0.0.0.0/0", Gateway: "192.0.2.1"}}}}
	err := setupNetwork(interfaces,
		func(name string) error { events = append(events, "up "+name); return nil },
		func(name, cidr string) error { events = append(events, "address "+name+" "+cidr); return nil },
		func(name string, route RouteConfig) error {
			events = append(events, "route "+name+" "+route.Destination)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"up lo", "address lo 127.0.0.1/8", "up eth0", "address eth0 192.0.2.10/24", "address eth0 2001:db8::10/64", "route eth0 0.0.0.0/0"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestSetupNetworkStopsOnFailure(t *testing.T) {
	sentinel := errors.New("denied")
	var events []string
	err := setupNetwork([]InterfaceConfig{{Name: "eth0"}},
		func(name string) error { events = append(events, name); return sentinel },
		func(string, string) error { t.Fatal("address after link failure"); return nil },
		func(string, RouteConfig) error { t.Fatal("route after link failure"); return nil },
	)
	if !errors.Is(err, sentinel) || strings.Join(events, ",") != "lo" {
		t.Fatalf("error = %v, events = %v", err, events)
	}
}
