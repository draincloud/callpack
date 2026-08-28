package registry

import (
	"fmt"
	"net"
	"os"
)

func localAddress() (string, error) {
	if host, err := os.Hostname(); err == nil && host != "" {
		if ips, err := net.LookupIP(host); err == nil {
			if addr := routable(ips); addr != "" {
				return addr, nil
			}
		}
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("failed to read the local interfaces: %w", err)
	}

	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			ips = append(ips, ipnet.IP)
		}
	}
	if addr := routable(ips); addr != "" {
		return addr, nil
	}

	return "", fmt.Errorf("failed to detect a routable address, set Service.Address")
}

func routable(ips []net.IP) string {
	var v6 string
	for _, ip := range ips {
		if !ip.IsGlobalUnicast() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		if v6 == "" {
			v6 = ip.String()
		}
	}
	return v6
}
