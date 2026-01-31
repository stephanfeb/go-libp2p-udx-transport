package udxtransport

import (
	"context"
	"net"

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
	udxConn, err := l.mux.Accept(context.Background())
	if err != nil {
		return nil, err
	}

	// Build remote multiaddr from connection's remote address
	var remoteMaddr ma.Multiaddr
	if udpAddr, ok := udxConn.RemoteAddr().(*net.UDPAddr); ok {
		remoteMaddr, _ = toUDXMultiaddr(udpAddr.IP.String(), udpAddr.Port)
	}

	return &conn{
		udxConn:     udxConn,
		transport:   l.transport,
		localPeer:   l.transport.localPeer,
		localMaddr:  l.laddr,
		remoteMaddr: remoteMaddr,
	}, nil
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
