package udxtransport

import (
	"fmt"
	"net"
	"strconv"

	ma "github.com/multiformats/go-multiaddr"
)

// P_UDX is the multiaddr protocol code for UDX.
// Uses 0x0300 (private use range), matching dart-libp2p.
const P_UDX = 0x0300

func init() {
	if err := ma.AddProtocol(ma.Protocol{
		Name:  "udx",
		Code:  P_UDX,
		VCode: ma.CodeToVarint(P_UDX),
		Size:  0, // No value component
	}); err != nil {
		// Protocol may already be registered
	}
}

// isUDXMultiaddr returns true if the multiaddr contains a /udx component.
func isUDXMultiaddr(addr ma.Multiaddr) bool {
	found := false
	ma.ForEach(addr, func(c ma.Component) bool {
		if c.Protocol().Code == P_UDX {
			found = true
			return false
		}
		return true
	})
	return found
}

// fromUDXMultiaddr extracts host and port from a /ip4/<host>/udp/<port>/udx multiaddr.
func fromUDXMultiaddr(addr ma.Multiaddr) (host string, port int, err error) {
	var hostStr, portStr string

	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_IP4, ma.P_IP6:
			hostStr = c.Value()
		case ma.P_UDP:
			portStr = c.Value()
		}
		return true
	})

	if hostStr == "" {
		return "", 0, fmt.Errorf("no IP address in multiaddr %s", addr)
	}
	if portStr == "" {
		return "", 0, fmt.Errorf("no UDP port in multiaddr %s", addr)
	}

	port, err = strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portStr, err)
	}

	return hostStr, port, nil
}

// toUDXMultiaddr creates a /ip4/<host>/udp/<port>/udx or /ip6/<host>/udp/<port>/udx multiaddr.
func toUDXMultiaddr(host string, port int) (ma.Multiaddr, error) {
	proto := "ip4"
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		proto = "ip6"
	}
	return ma.NewMultiaddr(fmt.Sprintf("/%s/%s/udp/%d/udx", proto, host, port))
}
