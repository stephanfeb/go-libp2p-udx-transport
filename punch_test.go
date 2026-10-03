package udxtransport

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
)

// TestSimultaneousConnectDialsFromListenPort checks that a hole-punch dial
// leaves from the listener's port, which is the one the peer punches towards,
// and that other dials do not.
func TestSimultaneousConnectDialsFromListenPort(t *testing.T) {
	serverKey, serverID := generateKey(t)
	clientKey, _ := generateKey(t)

	serverTr, err := NewTransport(serverKey, createUpgrader(t, serverKey), nil)
	if err != nil {
		t.Fatal(err)
	}
	clientTr, err := NewTransport(clientKey, createUpgrader(t, clientKey), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientTr.Close()

	listenAddr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/udp/0/udx")
	serverLn, err := serverTr.Listen(listenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer serverLn.Close()
	clientLn, err := clientTr.Listen(listenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer clientLn.Close()
	_, clientListenPort, err := fromUDXMultiaddr(clientLn.Multiaddr())
	if err != nil {
		t.Fatal(err)
	}

	// dialPort dials the server and returns the client port the server saw.
	dialPort := func(ctx context.Context) int {
		t.Helper()
		accepted := make(chan tpt.CapableConn, 1)
		go func() {
			c, err := serverLn.Accept()
			if err != nil {
				t.Error("accept:", err)
				close(accepted)
				return
			}
			accepted <- c
		}()

		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		conn, err := clientTr.Dial(ctx, serverLn.Multiaddr(), serverID)
		if err != nil {
			t.Fatal("dial:", err)
		}
		defer conn.Close()

		select {
		case sc, ok := <-accepted:
			if !ok {
				t.FailNow()
			}
			defer sc.Close()
			_, port, err := fromUDXMultiaddr(sc.RemoteMultiaddr())
			if err != nil {
				t.Fatal(err)
			}
			return port
		case <-time.After(10 * time.Second):
			t.Fatal("server did not accept")
		}
		return 0
	}

	punch := network.WithSimultaneousConnect(context.Background(), true, "hole-punching")
	if got := dialPort(punch); got != clientListenPort {
		t.Fatalf("punch dial came from port %d, want listen port %d", got, clientListenPort)
	}
	if got := dialPort(context.Background()); got == clientListenPort {
		t.Fatalf("ordinary dial came from the listen port %d", got)
	}

	// Without a listener a punch falls back to the ephemeral socket.
	clientLn.Close()
	if got := dialPort(punch); got == clientListenPort {
		t.Fatalf("punch dial used the closed listener's port %d", got)
	}
}
