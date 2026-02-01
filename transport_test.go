package udxtransport

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"testing"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	ma "github.com/multiformats/go-multiaddr"
)

func generateKey(t *testing.T) (ic.PrivKey, peer.ID) {
	t.Helper()
	priv, _, err := ic.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, id
}

func createUpgrader(t *testing.T, key ic.PrivKey) tpt.Upgrader {
	t.Helper()

	muxers := []upgrader.StreamMuxer{{
		ID:    yamux.ID,
		Muxer: yamux.DefaultTransport,
	}}

	noiseTpt, err := noise.New(noise.ID, key, muxers)
	if err != nil {
		t.Fatal(err)
	}

	u, err := upgrader.New(
		[]sec.SecureTransport{noiseTpt},
		muxers,
		nil,
		&network.NullResourceManager{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestCanDial(t *testing.T) {
	key, _ := generateKey(t)
	u := createUpgrader(t, key)
	tr, err := NewTransport(key, u, nil)
	if err != nil {
		t.Fatal(err)
	}

	udxAddr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/udp/1234/udx")
	tcpAddr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")

	if !tr.CanDial(udxAddr) {
		t.Fatal("should be able to dial /udx address")
	}
	if tr.CanDial(tcpAddr) {
		t.Fatal("should not be able to dial /tcp address")
	}
}

func TestProtocols(t *testing.T) {
	key, _ := generateKey(t)
	u := createUpgrader(t, key)
	tr, _ := NewTransport(key, u, nil)

	protos := tr.Protocols()
	if len(protos) != 1 || protos[0] != P_UDX {
		t.Fatalf("protocols: got %v, want [%d]", protos, P_UDX)
	}
}

func TestProxy(t *testing.T) {
	key, _ := generateKey(t)
	u := createUpgrader(t, key)
	tr, _ := NewTransport(key, u, nil)
	if tr.Proxy() {
		t.Fatal("UDX is not a proxy transport")
	}
}

func TestListenAndDial(t *testing.T) {
	serverKey, serverID := generateKey(t)
	clientKey, _ := generateKey(t)

	// Create server transport with Noise + Yamux upgrader
	serverU := createUpgrader(t, serverKey)
	serverTr, err := NewTransport(serverKey, serverU, nil)
	if err != nil {
		t.Fatal(err)
	}

	listenAddr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/udp/0/udx")
	ln, err := serverTr.Listen(listenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	actualAddr := ln.Multiaddr()
	t.Logf("Listening on %s", actualAddr)

	// Create client transport with Noise + Yamux upgrader
	clientU := createUpgrader(t, clientKey)
	clientTr, err := NewTransport(clientKey, clientU, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Accept in background — server conn kept alive until test ends
	type serverResult struct {
		conn tpt.CapableConn
		err  error
	}
	echoDone := make(chan error, 1)
	serverReady := make(chan serverResult, 1)
	go func() {
		serverConn, err := ln.Accept()
		if err != nil {
			serverReady <- serverResult{nil, fmt.Errorf("accept conn: %w", err)}
			return
		}
		serverReady <- serverResult{serverConn, nil}

		// Accept a stream (this is a Yamux stream now)
		s, err := serverConn.AcceptStream()
		if err != nil {
			echoDone <- fmt.Errorf("accept stream: %w", err)
			return
		}

		// Echo data back
		buf := make([]byte, 1024)
		n, err := s.Read(buf)
		if err != nil && err != io.EOF {
			echoDone <- fmt.Errorf("read: %w", err)
			return
		}
		_, err = s.Write(buf[:n])
		if err != nil {
			echoDone <- err
			return
		}
		s.Close()
		echoDone <- nil
	}()

	// Dial
	clientConn, err := clientTr.Dial(ctx, actualAddr, serverID)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer clientConn.Close()

	t.Log("Dial succeeded, peer:", clientConn.RemotePeer())

	// Wait for server to accept the connection
	sr := <-serverReady
	if sr.err != nil {
		t.Fatal("server accept:", sr.err)
	}
	defer sr.conn.Close()
	t.Log("Server accepted connection")

	// Verify connection properties
	if clientConn.LocalPeer() != clientTr.localPeer {
		t.Fatal("local peer mismatch")
	}
	if clientConn.RemotePeer() != serverID {
		t.Fatal("remote peer mismatch")
	}

	// Open a stream (Yamux stream) and send data
	s, err := clientConn.OpenStream(ctx)
	if err != nil {
		t.Fatal("open stream:", err)
	}

	testData := []byte("hello from libp2p over UDX with Noise+Yamux!")
	_, err = s.Write(testData)
	if err != nil {
		t.Fatal("write:", err)
	}
	s.CloseWrite()

	// Read echo
	buf := make([]byte, 1024)
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal("read echo:", err)
	}
	if string(buf[:n]) != string(testData) {
		t.Fatalf("echo mismatch: got %q, want %q", buf[:n], testData)
	}

	// Wait for server echo goroutine
	select {
	case err := <-echoDone:
		if err != nil {
			t.Fatalf("server error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server timed out")
	}

	t.Log("Listen → Dial → Noise → Yamux → Stream echo test PASSED")
}

func TestMultiaddr(t *testing.T) {
	addr, err := toUDXMultiaddr("127.0.0.1", 9090)
	if err != nil {
		t.Fatal(err)
	}
	if addr.String() != "/ip4/127.0.0.1/udp/9090/udx" {
		t.Fatalf("multiaddr: got %s", addr)
	}

	host, port, err := fromUDXMultiaddr(addr)
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" || port != 9090 {
		t.Fatalf("parsed: host=%s port=%d", host, port)
	}
}
