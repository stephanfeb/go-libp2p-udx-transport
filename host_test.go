package udxtransport

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
)

func makeHost(t *testing.T, listenAddr string) host.Host {
	t.Helper()
	h, err := libp2p.New(
		libp2p.NoTransports,
		libp2p.Transport(NewTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Muxer(yamux.ID, yamux.DefaultTransport),
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.ResourceManager(&network.NullResourceManager{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHostEcho(t *testing.T) {
	server := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer server.Close()

	const proto = "/echo/1.0.0"

	server.SetStreamHandler(proto, func(s network.Stream) {
		defer s.Close()
		io.Copy(s, s)
	})

	client := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer client.Close()

	serverInfo := peer.AddrInfo{
		ID:    server.ID(),
		Addrs: server.Addrs(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Connect(ctx, serverInfo); err != nil {
		t.Fatal("connect:", err)
	}

	s, err := client.NewStream(ctx, server.ID(), proto)
	if err != nil {
		t.Fatal("new stream:", err)
	}

	msg := []byte("hello from go-libp2p host over UDX!")
	_, err = s.Write(msg)
	if err != nil {
		t.Fatal("write:", err)
	}
	s.CloseWrite()

	buf := make([]byte, 1024)
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal("read:", err)
	}
	if string(buf[:n]) != string(msg) {
		t.Fatalf("echo mismatch: got %q, want %q", buf[:n], msg)
	}
	t.Log("Host-level echo over UDX PASSED")
}
