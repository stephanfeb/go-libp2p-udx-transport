package udxtransport

import (
	"context"
	"log"
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
	// Retry loop: per-connection errors (upgrade failures, stream errors) must
	// NOT propagate to the caller. The go-libp2p swarm treats any error from
	// Accept() as fatal and shuts down the entire listener. In UDX, the listener
	// wraps a single shared multiplexer — closing it kills ALL connections and
	// stops all UDP listening. Only multiplexer-level errors (closed) are fatal.
	for {
		ctx := context.Background()

		udxConn, err := l.mux.Accept(ctx)
		if err != nil {
			// Multiplexer closed or context cancelled — truly fatal
			return nil, err
		}

		// Accept stream 0 from the dialer (the upgrade stream)
		stream0, err := udxConn.AcceptStream(ctx)
		if err != nil {
			log.Printf("[UDX-DIAG] listener.Accept: AcceptStream failed from %v: %v (retrying)", udxConn.RemoteAddr(), err)
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
			log.Printf("[UDX-DIAG] listener.Accept: OpenConnection scope failed from %v: %v (retrying)", udxConn.RemoteAddr(), err)
			rawConn.Close()
			continue
		}

		// Upgrader handles inbound Noise + Yamux negotiation
		log.Printf("[UDX-DIAG] listener.Accept: upgrading connection from %v", udxConn.RemoteAddr())
		conn, err := l.transport.upgrader.Upgrade(ctx, l.transport, rawConn, network.DirInbound, "", connScope)
		if err != nil {
			log.Printf("[UDX-DIAG] listener.Accept: Upgrade FAILED from %v: %v (retrying)", udxConn.RemoteAddr(), err)
			rawConn.Close()
			continue
		}
		log.Printf("[UDX-DIAG] listener.Accept: Upgrade SUCCESS from %v, remotePeer=%s", udxConn.RemoteAddr(), conn.RemotePeer())
		return conn, nil
	}
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