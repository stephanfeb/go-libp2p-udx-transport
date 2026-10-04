package udxtransport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
)

// TestDialsFromListenPort checks which socket each kind of dial leaves from.
// Ordinary dials and hole punches use the listener's port, so the peer
// observes the address this host listens on. The uncoordinated direct dial
// before a punch keeps an ephemeral port.
func TestDialsFromListenPort(t *testing.T) {
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

	punch := network.WithForceDirectDial(
		network.WithSimultaneousConnect(context.Background(), true, "hole-punching"), "hole-punching")
	if got := dialPort(punch); got != clientListenPort {
		t.Fatalf("punch dial came from port %d, want listen port %d", got, clientListenPort)
	}
	if got := dialPort(context.Background()); got != clientListenPort {
		t.Fatalf("ordinary dial came from port %d, want listen port %d", got, clientListenPort)
	}
	preDial := network.WithForceDirectDial(context.Background(), "hole-punching")
	if got := dialPort(preDial); got == clientListenPort {
		t.Fatalf("uncoordinated direct dial came from the listen port %d", got)
	}

	// Without a listener every dial falls back to the ephemeral socket.
	clientLn.Close()
	if got := dialPort(punch); got == clientListenPort {
		t.Fatalf("punch dial used the closed listener's port %d", got)
	}
	if got := dialPort(context.Background()); got == clientListenPort {
		t.Fatalf("ordinary dial used the closed listener's port %d", got)
	}
}

func TestIsOwnAddr(t *testing.T) {
	key, _ := generateKey(t)
	tr, err := NewTransport(key, createUpgrader(t, key), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	for _, tc := range []struct {
		listen string
		dial   string
		own    bool
	}{
		{"/ip4/127.0.0.1/udp/0/udx", "127.0.0.1", true},
		{"/ip4/127.0.0.1/udp/0/udx", "127.0.0.2", false},
		{"/ip4/0.0.0.0/udp/0/udx", "127.0.0.1", true},
		{"/ip4/0.0.0.0/udp/0/udx", "0.0.0.0", true},
		{"/ip4/0.0.0.0/udp/0/udx", "203.0.113.7", false},
	} {
		laddr, _ := ma.NewMultiaddr(tc.listen)
		ln, err := tr.Listen(laddr)
		if err != nil {
			t.Fatal(err)
		}
		raw := tr.listenerFor("udp4")
		port := raw.mux.Addr().(*net.UDPAddr).Port
		if got := raw.isOwnAddr(&net.UDPAddr{IP: net.ParseIP(tc.dial), Port: port}); got != tc.own {
			t.Errorf("listen %s, dial %s: own = %v, want %v", tc.listen, tc.dial, got, tc.own)
		}
		if raw.isOwnAddr(&net.UDPAddr{IP: net.ParseIP(tc.dial), Port: port + 1}) {
			t.Errorf("listen %s, dial %s on another port: own = true", tc.listen, tc.dial)
		}
		ln.Close()
	}
}
