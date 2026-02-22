package udxtransport

import (
	"log"
	"net"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	udx "github.com/stephanfeb/go-udx"
)

// streamConn wraps a UDX stream (stream 0) as a net.Conn with multiaddr info.
// This is passed to the go-libp2p upgrader which layers Noise + Yamux on top.
type streamConn struct {
	stream      *udx.Stream
	connection  *udx.Connection
	localMaddr  ma.Multiaddr
	remoteMaddr ma.Multiaddr
}

// net.Conn interface

func (sc *streamConn) Read(p []byte) (int, error)  { return sc.stream.Read(p) }
func (sc *streamConn) Write(p []byte) (int, error) { return sc.stream.Write(p) }

func (sc *streamConn) Close() error {
	log.Printf("[UDX-DIAG] streamConn.Close() remoteAddr=%v", sc.connection.RemoteAddr())
	sc.stream.Close()
	return sc.connection.Close()
}

func (sc *streamConn) LocalAddr() net.Addr  { return sc.connection.LocalAddr() }
func (sc *streamConn) RemoteAddr() net.Addr { return sc.connection.RemoteAddr() }

func (sc *streamConn) SetDeadline(t time.Time) error {
	if err := sc.stream.SetReadDeadline(t); err != nil {
		return err
	}
	return sc.stream.SetWriteDeadline(t)
}

func (sc *streamConn) SetReadDeadline(t time.Time) error {
	return sc.stream.SetReadDeadline(t)
}

func (sc *streamConn) SetWriteDeadline(t time.Time) error {
	return sc.stream.SetWriteDeadline(t)
}

// manet.Conn interface (multiaddr-aware net.Conn)

func (sc *streamConn) LocalMultiaddr() ma.Multiaddr  { return sc.localMaddr }
func (sc *streamConn) RemoteMultiaddr() ma.Multiaddr { return sc.remoteMaddr }
