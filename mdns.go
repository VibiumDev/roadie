package main

import (
	"net"

	"github.com/hashicorp/mdns"
)

// RegisterMDNS advertises the Roadie service via Bonjour/mDNS and responds
// to A record queries for <name>.local so browsers can reach us by name.
// Returns a shutdown function to call on exit.
func RegisterMDNS(name string, port int, resolution string) (shutdown func(), err error) {
	ips := localIPs()

	service, err := mdns.NewMDNSService(
		name,
		"_roadie._tcp",
		"",
		name+".local.",
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
