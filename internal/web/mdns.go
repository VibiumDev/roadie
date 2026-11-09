package web

import (
	"github.com/VibiumDev/roadie/internal/platform"
	"github.com/hashicorp/mdns"
	"net"
)

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
		platform.MDNSHostname(name)+".local.",
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
