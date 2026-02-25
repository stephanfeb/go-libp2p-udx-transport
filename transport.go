package udxtransport

import (
	"context"
	"fmt"
	"net"
	"sync"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	udx "github.com/stephanfeb/go-udx"
)

// outboundMux holds a shared UDP socket and multiplexer for outbound connections.
type outboundMux struct {
	conn *net.UDPConn
	mux  *udx.Multiplexer
}

// Transport implements the go-libp2p Transport interface using UDX.
type Transport struct {
	privKey   ic.PrivKey
	localPeer peer.ID
	upgrader  tpt.Upgrader
	rcmgr     network.ResourceManager

	mu         sync.Mutex
	outboundV4 *outboundMux // lazily created on first IPv4 dial
	outboundV6 *outboundMux // lazily created on first IPv6 dial
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

// getOutboundMux returns the shared outbound multiplexer for the given UDP
// network ("udp4" or "udp6"), creating it lazily on first use.
func (t *Transport) getOutboundMux(udpNetwork string) (*udx.Multiplexer, ma.Multiaddr, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	isV6 := (udpNetwork == "udp6")
	om := t.outboundV4
	if isV6 {
		om = t.outboundV6
	}
	if om != nil {
		localUDP := om.conn.LocalAddr().(*net.UDPAddr)
		localMaddr, _ := toUDXMultiaddr(localUDP.IP.String(), localUDP.Port)
		return om.mux, localMaddr, nil
	}

	// Bind ephemeral port once
	localConn, err := net.ListenUDP(udpNetwork, nil)
	if err != nil {
		return nil, nil, err
	}
	mux := udx.NewMultiplexer(localConn, udx.RealClock{})
	om = &outboundMux{conn: localConn, mux: mux}

	if isV6 {
		t.outboundV6 = om
	} else {
		t.outboundV4 = om
	}

	localUDP := localConn.LocalAddr().(*net.UDPAddr)
	localMaddr, _ := toUDXMultiaddr(localUDP.IP.String(), localUDP.Port)
	return mux, localMaddr, nil
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

	// Match address family of remote
	udpNetwork := "udp4"
	if remoteAddr.IP.To4() == nil {
		udpNetwork = "udp6"
	}

	mux, localMaddr, err := t.getOutboundMux(udpNetwork)
	if err != nil {
		return nil, fmt.Errorf("outbound mux: %w", err)
	}

	udxConn, err := mux.Dial(ctx, remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("dialing: %w", err)
	}

	// Open stream 0 as the raw connection for the upgrader
	stream0, err := udxConn.OpenStream(ctx)
	if err != nil {
		udxConn.Close()
		return nil, fmt.Errorf("opening upgrade stream: %w", err)
	}

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

// Close shuts down the shared outbound multiplexers and their UDP sockets.
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.outboundV4 != nil {
		t.outboundV4.mux.Close()
		t.outboundV4 = nil
	}
	if t.outboundV6 != nil {
		t.outboundV6.mux.Close()
		t.outboundV6 = nil
	}
	return nil
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

	// Explicitly select address family to avoid dual-stack surprises on Linux.
	udpNetwork := "udp4"
	if udpAddr.IP.To4() == nil {
		udpNetwork = "udp6"
	}
	udpConn, err := net.ListenUDP(udpNetwork, udpAddr)
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
