package goboot

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

const netlinkHeaderSize = 16

// SetInterfaceUp brings a named network interface up using rtnetlink.
func SetInterfaceUp(name string) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("find interface %q: %w", name, err)
	}
	if iface.Flags&net.FlagUp != 0 {
		return nil
	}
	data := make([]byte, unix.SizeofIfInfomsg)
	binary.NativeEndian.PutUint32(data[4:8], uint32(iface.Index))
	binary.NativeEndian.PutUint32(data[8:12], unix.IFF_UP)
	binary.NativeEndian.PutUint32(data[12:16], unix.IFF_UP)
	if err := sendRouteMessage(unix.RTM_NEWLINK, unix.NLM_F_REQUEST|unix.NLM_F_ACK, data); err != nil {
		return fmt.Errorf("bring interface %q up: %w", name, err)
	}
	return nil
}

// AddAddress assigns an IPv4 or IPv6 CIDR address to a named interface.
// An address already present is accepted.
func AddAddress(name, cidr string) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("find interface %q: %w", name, err)
	}
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("address %q: %w", cidr, err)
	}
	bits, _ := network.Mask.Size()
	family, address := ipBytes(ip)
	data := make([]byte, unix.SizeofIfAddrmsg)
	data[0] = family
	data[1] = byte(bits)
	if ip.IsLoopback() {
		data[3] = unix.RT_SCOPE_HOST
	} else if ip.IsLinkLocalUnicast() {
		data[3] = unix.RT_SCOPE_LINK
	}
	binary.NativeEndian.PutUint32(data[4:8], uint32(iface.Index))
	data = appendRouteAttribute(data, unix.IFA_LOCAL, address)
	data = appendRouteAttribute(data, unix.IFA_ADDRESS, address)
	err = sendRouteMessage(unix.RTM_NEWADDR, unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_EXCL, data)
	if errors.Is(err, syscall.EEXIST) {
		addresses, lookupErr := iface.Addrs()
		if lookupErr != nil {
			return fmt.Errorf("check existing address on %q: %w", name, lookupErr)
		}
		wanted := fmt.Sprintf("%s/%d", ip.String(), bits)
		for _, current := range addresses {
			if current.String() == wanted {
				return nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("add address %q to %q: %w", cidr, name, err)
	}
	return nil
}

// AddRoute adds one IPv4 or IPv6 route through the named interface. A route
// with an existing destination is reported as an error for caller review.
func AddRoute(name string, route RouteConfig) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("find interface %q: %w", name, err)
	}
	_, network, err := net.ParseCIDR(route.Destination)
	if err != nil {
		return fmt.Errorf("route destination %q: %w", route.Destination, err)
	}
	ones, _ := network.Mask.Size()
	family, destination := ipBytes(network.IP)
	data := make([]byte, unix.SizeofRtMsg)
	data[0] = family
	data[1] = byte(ones)
	data[4] = unix.RT_TABLE_MAIN
	data[5] = unix.RTPROT_STATIC
	data[6] = unix.RT_SCOPE_LINK
	data[7] = unix.RTN_UNICAST
	if ones != 0 {
		data = appendRouteAttribute(data, unix.RTA_DST, destination)
	}
	if route.Gateway != "" {
		gateway := net.ParseIP(route.Gateway)
		if gateway == nil {
			return fmt.Errorf("route gateway %q: invalid IP", route.Gateway)
		}
		gatewayFamily, gatewayBytes := ipBytes(gateway)
		if gatewayFamily != family {
			return fmt.Errorf("route gateway %q: IP family differs from destination", route.Gateway)
		}
		data[6] = unix.RT_SCOPE_UNIVERSE
		data = appendRouteAttribute(data, unix.RTA_GATEWAY, gatewayBytes)
	}
	index := make([]byte, 4)
	binary.NativeEndian.PutUint32(index, uint32(iface.Index))
	data = appendRouteAttribute(data, unix.RTA_OIF, index)
	if err := sendRouteMessage(unix.RTM_NEWROUTE, unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_EXCL, data); err != nil {
		return fmt.Errorf("add route %q through %q: %w", route.Destination, name, err)
	}
	return nil
}

// SetupNetwork configures loopback, then each static interface and its routes.
// DHCP is not configured.
func SetupNetwork(interfaces []InterfaceConfig) error {
	return setupNetwork(interfaces, SetInterfaceUp, AddAddress, AddRoute)
}

func setupNetwork(interfaces []InterfaceConfig, up func(string) error, address func(string, string) error, route func(string, RouteConfig) error) error {
	if err := up("lo"); err != nil {
		return err
	}
	if err := address("lo", "127.0.0.1/8"); err != nil {
		return err
	}
	for _, iface := range interfaces {
		if iface.Name == "lo" {
			// Loopback's baseline was set above; additional addresses are valid.
		} else if err := up(iface.Name); err != nil {
			return err
		}
		for _, cidr := range iface.Addresses {
			if err := address(iface.Name, cidr); err != nil {
				return err
			}
		}
		for _, item := range iface.Routes {
			if err := route(iface.Name, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func ipBytes(ip net.IP) (byte, []byte) {
	if ip4 := ip.To4(); ip4 != nil {
		return unix.AF_INET, ip4
	}
	return unix.AF_INET6, ip.To16()
}

func appendRouteAttribute(data []byte, kind uint16, value []byte) []byte {
	length := 4 + len(value)
	attr := make([]byte, (length+3)&^3)
	binary.NativeEndian.PutUint16(attr[0:2], uint16(length))
	binary.NativeEndian.PutUint16(attr[2:4], kind)
	copy(attr[4:], value)
	return append(data, attr...)
}

func sendRouteMessage(kind uint16, flags uint16, payload []byte) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 5}); err != nil {
		return err
	}
	message := make([]byte, netlinkHeaderSize+len(payload))
	binary.NativeEndian.PutUint32(message[0:4], uint32(len(message)))
	binary.NativeEndian.PutUint16(message[4:6], kind)
	binary.NativeEndian.PutUint16(message[6:8], flags)
	binary.NativeEndian.PutUint32(message[8:12], 1)
	copy(message[netlinkHeaderSize:], payload)
	if err := unix.Sendto(fd, message, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buffer := make([]byte, 65536)
	for {
		n, _, err := unix.Recvfrom(fd, buffer, 0)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		messages, err := syscall.ParseNetlinkMessage(buffer[:n])
		if err != nil {
			return err
		}
		for _, reply := range messages {
			if reply.Header.Seq != 1 {
				continue
			}
			if reply.Header.Type == unix.NLMSG_ERROR {
				if len(reply.Data) < 4 {
					return errors.New("short netlink acknowledgement")
				}
				code := int32(binary.NativeEndian.Uint32(reply.Data[:4]))
				if code == 0 {
					return nil
				}
				return syscall.Errno(-code)
			}
		}
	}
}
