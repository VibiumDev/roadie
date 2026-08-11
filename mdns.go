package main

import (
	"net"
	"strings"

	"github.com/hashicorp/mdns"
)

// MDNSHostname returns the hostname advertised for a service name.
//
// The responder matches queried names exactly, while browsers lowercase
// hostnames before resolving them — so a mixed-case name would advertise a
// host that the browser then fails to look up. Lowercasing here means any
// capitalization works: --name Android is reachable at android.local.
func MDNSHostname(name string) string {
	return strings.ToLower(name)
}

// RegisterMDNS advertises the Roadie service via Bonjour/mDNS and responds
// to A record queries for <name>.local so browsers can reach us by name.
// The service instance keeps the name as given, since that is a display name
// browsers never resolve; only the hostname is normalized.
// Returns a shutdown function to call on exit.
func RegisterMDNS(name string, port int, resolution string) (shutdown func(), err error) {
	ips := localIPs()

	service, err := mdns.NewMDNSService(
		name,
		"_roadie._tcp",
		"",
		MDNSHostname(name)+".local.",
		port,
		ips,
		[]string{"version=0.1", "resolution=" + resolution},
	)
	if err != nil {
		return nil, err
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return nil, err
	}

	return func() { server.Shutdown() }, nil
}

// localIPs returns all non-loopback IPv4 addresses.
func localIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			ips = append(ips, ipNet.IP)
		}
	}
	return ips
}
