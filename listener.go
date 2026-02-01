package udxtransport

import (
	"context"
	"net"

	"github.com/libp2p/go-libp2p/core/network"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	udx "github.com/stephanfeb/go-udx"
)

// listener wraps a udx.Multiplexer to implement transport.Listener.
type listener struct {
	mux       *udx.Multiplexer
	transport *Transport
	laddr     ma.Multiaddr
}

var _ tpt.Listener = (*listener)(nil)

func (l *listener) Accept() (tpt.CapableConn, error) {
	ctx := context.Background()

	udxConn, err := l.mux.Accept(ctx)
	if err != nil {
		return nil, err
	}

	// Accept stream 0 from the dialer (the upgrade stream)
	stream0, err := udxConn.AcceptStream(ctx)
	if err != nil {
		udxConn.Close()
		return nil, err
	}

	// Build remote multiaddr from connection's remote address
	var remoteMaddr ma.Multiaddr
	if udpAddr, ok := udxConn.RemoteAddr().(*net.UDPAddr); ok {
		remoteMaddr, _ = toUDXMultiaddr(udpAddr.IP.String(), udpAddr.Port)
	}

	rawConn := &streamConn{
		stream:      stream0,
		connection:  udxConn,
		localMaddr:  l.laddr,
		remoteMaddr: remoteMaddr,
	}

	// Get a connection scope from the resource manager
	connScope, err := l.transport.rcmgr.OpenConnection(network.DirInbound, false, remoteMaddr)
	if err != nil {
		rawConn.Close()
		return nil, err
	}

	// Upgrader handles inbound Noise + Yamux negotiation
	return l.transport.upgrader.Upgrade(ctx, l.transport, rawConn, network.DirInbound, "", connScope)
}

func (l *listener) Close() error {
	return l.mux.Close()
}

func (l *listener) Addr() net.Addr {
	return l.mux.Addr()
}

func (l *listener) Multiaddr() ma.Multiaddr {
	return l.laddr
}
