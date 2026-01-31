package udxtransport

import (
	"context"
	"io"
	"testing"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func generateKey(t *testing.T) (ic.PrivKey, peer.ID) {
	t.Helper()
	priv, _, err := ic.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, id
}

func TestCanDial(t *testing.T) {
	key, _ := generateKey(t)
	tr, err := NewTransport(key, nil)
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
	tr, _ := NewTransport(key, nil)

	protos := tr.Protocols()
	if len(protos) != 1 || protos[0] != P_UDX {
		t.Fatalf("protocols: got %v, want [%d]", protos, P_UDX)
	}
}

func TestProxy(t *testing.T) {
	key, _ := generateKey(t)
	tr, _ := NewTransport(key, nil)
	if tr.Proxy() {
		t.Fatal("UDX is not a proxy transport")
	}
}

func TestListenAndDial(t *testing.T) {
	serverKey, serverID := generateKey(t)
	clientKey, _ := generateKey(t)

	// Create server transport and listen
	serverTr, err := NewTransport(serverKey, nil)
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

	// Create client transport and dial
	clientTr, err := NewTransport(clientKey, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Accept in background
	acceptDone := make(chan error, 1)
	go func() {
		serverConn, err := ln.Accept()
		if err != nil {
			acceptDone <- err
			return
		}
		defer serverConn.Close()

		// Accept a stream
		s, err := serverConn.AcceptStream()
		if err != nil {
			acceptDone <- err
			return
		}

		// Echo data back
		buf := make([]byte, 1024)
		n, err := s.Read(buf)
		if err != nil && err != io.EOF {
			acceptDone <- err
			return
		}
		_, err = s.Write(buf[:n])
		if err != nil {
			acceptDone <- err
			return
		}
		s.Close()
		acceptDone <- nil
	}()

	// Dial
	clientConn, err := clientTr.Dial(ctx, actualAddr, serverID)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	// Verify connection properties
	if clientConn.LocalPeer() != clientTr.localPeer {
		t.Fatal("local peer mismatch")
	}
	if clientConn.RemotePeer() != serverID {
		t.Fatal("remote peer mismatch")
	}

	// Open a stream and send data
	s, err := clientConn.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}

	testData := []byte("hello from libp2p over UDX!")
	_, err = s.Write(testData)
	if err != nil {
		t.Fatal(err)
	}
	s.CloseWrite()

	// Read echo
	buf := make([]byte, 1024)
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if string(buf[:n]) != string(testData) {
		t.Fatalf("echo mismatch: got %q, want %q", buf[:n], testData)
	}

	// Wait for server
	select {
	case err := <-acceptDone:
		if err != nil {
			t.Fatalf("server error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server timed out")
	}

	t.Log("Listen → Dial → Stream echo test PASSED")
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
