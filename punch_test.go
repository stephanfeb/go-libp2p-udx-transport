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
		raw := tr.listeners[0]
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

func TestListenerFor(t *testing.T) {
	key, _ := generateKey(t)
	tr, err := NewTransport(key, createUpgrader(t, key), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	listen := func(addr string) *rawListener {
		t.Helper()
		laddr, _ := ma.NewMultiaddr(addr)
		ln, err := tr.Listen(laddr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		return tr.listeners[len(tr.listeners)-1]
	}
	remote := func(ip string) *net.UDPAddr { return &net.UDPAddr{IP: net.ParseIP(ip), Port: 4001} }

	loop := listen("/ip4/127.0.0.1/udp/0/udx")
	if got := tr.listenerFor("udp4", remote("127.0.0.1")); got != loop {
		t.Errorf("loopback target: got %v, want the loopback listener", got)
	}
	if got := tr.listenerFor("udp4", remote("203.0.113.7")); got != nil {
		t.Errorf("public target with only a loopback listener: got %v, want nil", got)
	}
	if got := tr.listenerFor("udp6", remote("127.0.0.1")); got != nil {
		t.Errorf("udp6 with only a udp4 listener: got %v, want nil", got)
	}

	all := listen("/ip4/0.0.0.0/udp/0/udx")
	if got := tr.listenerFor("udp4", remote("203.0.113.7")); got != all {
		t.Errorf("public target: got %v, want the unspecified listener", got)
	}
	if got := tr.listenerFor("udp4", remote("127.0.0.1")); got != all {
		t.Errorf("loopback target: got %v, want the unspecified listener", got)
	}

	// A dial to a listener's own address uses another listener.
	own := loop.mux.Addr().(*net.UDPAddr)
	if got := tr.listenerFor("udp4", own); got != all {
		t.Errorf("own address of the loopback listener: got %v, want the unspecified listener", got)
	}
	allPort := all.mux.Addr().(*net.UDPAddr).Port
	if got := tr.listenerFor("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: allPort}); got != loop {
		t.Errorf("own address of the unspecified listener: got %v, want the loopback listener", got)
	}
}

// TestLoopbackListenerDialsLAN checks that a host that listens only on
// loopback dials a non-loopback address from an ephemeral socket. A
// loopback socket cannot send to a remote host. The test dials this
// machine's own LAN address, which a loopback socket can reach on some
// systems, so it checks the port the server sees and not only the dial.
func TestLoopbackListenerDialsLAN(t *testing.T) {
	lan := lanIPv4(t)

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

	allAddr, _ := ma.NewMultiaddr("/ip4/0.0.0.0/udp/0/udx")
	serverLn, err := serverTr.Listen(allAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer serverLn.Close()
	loopAddr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/udp/0/udx")
	clientLn, err := clientTr.Listen(loopAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer clientLn.Close()
	_, clientListenPort, err := fromUDXMultiaddr(clientLn.Multiaddr())
	if err != nil {
		t.Fatal(err)
	}

	_, serverPort, err := fromUDXMultiaddr(serverLn.Multiaddr())
	if err != nil {
		t.Fatal(err)
	}
	target, _ := toUDXMultiaddr(lan.String(), serverPort)

	accepted := make(chan tpt.CapableConn, 1)
	go func() {
		c, err := serverLn.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := clientTr.Dial(ctx, target, serverID)
	if err != nil {
		t.Fatalf("dial %s from a loopback-only host: %v", target, err)
	}
	defer conn.Close()

	select {
	case sc, ok := <-accepted:
		if !ok {
			t.Fatal("server accept failed")
		}
		defer sc.Close()
		_, port, err := fromUDXMultiaddr(sc.RemoteMultiaddr())
		if err != nil {
			t.Fatal(err)
		}
		if port == clientListenPort {
			t.Fatalf("dial to %s left from the loopback listener's port %d", lan, port)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not accept")
	}
}

// lanIPv4 returns a non-loopback IPv4 address of this machine, or skips
// the test if there is none.
func lanIPv4(t *testing.T) net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skip("no interface addresses:", err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			return n.IP.To4()
		}
	}
	t.Skip("no non-loopback IPv4 address")
	return nil
}
