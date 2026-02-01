package udxtransport

import (
	"context"
	"fmt"
	"net"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	udx "github.com/stephanfeb/go-udx"
)

// Transport implements the go-libp2p Transport interface using UDX.
type Transport struct {
	privKey   ic.PrivKey
	localPeer peer.ID
	upgrader  tpt.Upgrader
	rcmgr     network.ResourceManager
}

var _ tpt.Transport = (*Transport)(nil)

// NewTransport creates a new UDX transport with the given upgrader.
// The upgrader handles security (Noise) and stream muxing (Yamux).
func NewTransport(key ic.PrivKey, u tpt.Upgrader, rcmgr network.ResourceManager) (*Transport, error) {
	localPeer, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("deriving peer ID: %w", err)
	}

	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}

	return &Transport{
		privKey:   key,
		localPeer: localPeer,
		upgrader:  u,
		rcmgr:     rcmgr,
	}, nil
}

// Dial dials a remote peer over UDX.
func (t *Transport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	host, port, err := fromUDXMultiaddr(raddr)
	if err != nil {
		return nil, fmt.Errorf("parsing multiaddr: %w", err)
	}

	remoteAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return nil, fmt.Errorf("resolving address: %w", err)
	}

	// Bind local UDP socket (match address family of remote)
	udpNetwork := "udp4"
	if remoteAddr.IP.To4() == nil {
		udpNetwork = "udp6"
	}
	localConn, err := net.ListenUDP(udpNetwork, nil)
	if err != nil {
		return nil, fmt.Errorf("binding local socket: %w", err)
	}

	mux := udx.NewMultiplexer(localConn, udx.RealClock{})
	udxConn, err := mux.Dial(ctx, remoteAddr)
	if err != nil {
		mux.Close()
		return nil, fmt.Errorf("dialing: %w", err)
	}

	// Open stream 0 as the raw connection for the upgrader
	stream0, err := udxConn.OpenStream(ctx)
	if err != nil {
		mux.Close()
		return nil, fmt.Errorf("opening upgrade stream: %w", err)
	}

	// Build local multiaddr
	localUDP := localConn.LocalAddr().(*net.UDPAddr)
	localMaddr, _ := toUDXMultiaddr(localUDP.IP.String(), localUDP.Port)

	rawConn := &streamConn{
		stream:      stream0,
		connection:  udxConn,
		localMaddr:  localMaddr,
		remoteMaddr: raddr,
	}

	// Get a connection scope from the resource manager
	connScope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("resource manager: %w", err)
	}

	// Upgrader handles Noise + Yamux negotiation
	return t.upgrader.Upgrade(ctx, t, rawConn, network.DirOutbound, p, connScope)
}

// Listen listens for incoming UDX connections.
func (t *Transport) Listen(laddr ma.Multiaddr) (tpt.Listener, error) {
	host, port, err := fromUDXMultiaddr(laddr)
	if err != nil {
		return nil, fmt.Errorf("parsing multiaddr: %w", err)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return nil, fmt.Errorf("resolving address: %w", err)
	}

	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listening: %w", err)
	}

	mux := udx.NewMultiplexer(udpConn, udx.RealClock{})

	// Build actual listen multiaddr (with resolved port if 0)
	actualAddr := udpConn.LocalAddr().(*net.UDPAddr)
	actualMaddr, _ := toUDXMultiaddr(actualAddr.IP.String(), actualAddr.Port)

	return &listener{
		mux:       mux,
		transport: t,
		laddr:     actualMaddr,
	}, nil
}

// CanDial returns true if this transport can dial the given multiaddr.
func (t *Transport) CanDial(addr ma.Multiaddr) bool {
	return isUDXMultiaddr(addr)
}

// Protocols returns the protocol codes handled by this transport.
func (t *Transport) Protocols() []int {
	return []int{P_UDX}
}

// Proxy returns false — UDX is not a proxy transport.
func (t *Transport) Proxy() bool {
	return false
}
