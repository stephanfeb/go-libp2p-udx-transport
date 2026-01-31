package udxtransport

import (
	"context"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	udx "github.com/stephanfeb/go-udx"
)

// conn wraps a udx.Connection to implement transport.CapableConn.
type conn struct {
	udxConn      *udx.Connection
	transport    *Transport
	localPeer    peer.ID
	remotePeer   peer.ID
	remotePubKey ic.PubKey
	localMaddr   ma.Multiaddr
	remoteMaddr  ma.Multiaddr
	scope        network.ConnManagementScope
}

var _ tpt.CapableConn = (*conn)(nil)

// --- network.MuxedConn ---

func (c *conn) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	s, err := c.udxConn.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	return &stream{udxStream: s}, nil
}

func (c *conn) AcceptStream() (network.MuxedStream, error) {
	s, err := c.udxConn.AcceptStream(context.Background())
	if err != nil {
		return nil, err
	}
	return &stream{udxStream: s}, nil
}

func (c *conn) Close() error {
	return c.udxConn.Close()
}

func (c *conn) CloseWithError(errCode network.ConnErrorCode) error {
	return c.udxConn.CloseWithError(uint32(errCode), "")
}

func (c *conn) IsClosed() bool {
	return c.udxConn.State() == udx.ConnStateClosed
}

func (c *conn) As(target any) bool {
	return false
}

// --- network.ConnSecurity ---

func (c *conn) LocalPeer() peer.ID          { return c.localPeer }
func (c *conn) RemotePeer() peer.ID         { return c.remotePeer }
func (c *conn) RemotePublicKey() ic.PubKey   { return c.remotePubKey }
func (c *conn) ConnState() network.ConnectionState {
	return network.ConnectionState{Transport: "udx"}
}

// --- network.ConnMultiaddrs ---

func (c *conn) LocalMultiaddr() ma.Multiaddr  { return c.localMaddr }
func (c *conn) RemoteMultiaddr() ma.Multiaddr { return c.remoteMaddr }

// --- network.ConnScoper ---

func (c *conn) Scope() network.ConnScope {
	if c.scope != nil {
		return c.scope
	}
	return &network.NullScope{}
}

// --- transport.CapableConn ---

func (c *conn) Transport() tpt.Transport { return c.transport }
