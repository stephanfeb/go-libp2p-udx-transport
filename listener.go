package udxtransport

import (
	"context"
	"net"

	"github.com/libp2p/go-libp2p/core/network"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	udx "github.com/stephanfeb/go-udx"
)

// rawListener wraps a udx.Multiplexer to implement transport.GatedMaListener.
// It accepts raw UDX connections and prepares them for the upgrader pipeline,
// which handles Noise + Yamux negotiation in parallel goroutines.
type rawListener struct {
	mux       *udx.Multiplexer
	transport *Transport
	laddr     ma.Multiaddr
}

var _ tpt.GatedMaListener = (*rawListener)(nil)

// Accept drains the UDX multiplexer and returns raw (unsecured, non-muxed)
// connections. The upgrader's handleIncoming goroutine calls this in a tight
// loop and spawns a goroutine per connection for the Noise + Yamux upgrade.
//
// Per-connection errors (stream failures, resource limits) are retried.
// Only multiplexer-level errors (closed) are fatal and returned to the caller.
func (l *rawListener) Accept() (manet.Conn, network.ConnManagementScope, error) {
	for {
		ctx := context.Background()

		udxConn, err := l.mux.Accept(ctx)
		if err != nil {
			return nil, nil, err
		}

		// Accept stream 0 from the dialer (the upgrade stream)
		stream0, err := udxConn.AcceptStream(ctx)
		if err != nil {
			udxConn.Close()
			continue
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
			continue
		}

		return rawConn, connScope, nil
	}
}

func (l *rawListener) Close() error {
	return l.mux.Close()
}

func (l *rawListener) Addr() net.Addr {
	return l.mux.Addr()
}

func (l *rawListener) Multiaddr() ma.Multiaddr {
	return l.laddr
}
