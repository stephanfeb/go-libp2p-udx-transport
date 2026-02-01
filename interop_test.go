package udxtransport

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const dartLibp2pDir = "../dart-libp2p"
const echoProto = "/echo/1.0.0"

// TestInteropDartServerGoClient starts a Dart libp2p echo server, then dials from Go.
func TestInteropDartServerGoClient(t *testing.T) {
	dartServer := exec.Command("dart", "run", "bin/interop_echo_server.dart")
	dartServer.Dir = dartLibp2pDir

	stderr, err := dartServer.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := dartServer.Start(); err != nil {
		t.Fatalf("start dart server: %v", err)
	}
	defer func() {
		dartServer.Process.Kill()
		dartServer.Wait()
	}()

	// Wait for READY <port> <peer-id>
	scanner := bufio.NewScanner(stderr)
	var serverPort int
	var serverPeerIDStr string
	ready := make(chan struct{})

	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			t.Logf("[dart-server] %s", line)
			if strings.HasPrefix(line, "READY ") {
				parts := strings.Fields(line)
				if len(parts) >= 3 {
					port, err := strconv.Atoi(parts[1])
					if err == nil {
						serverPort = port
						serverPeerIDStr = parts[2]
						close(ready)
					}
				}
			}
		}
	}()

	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("dart server did not become ready in 30s")
	}

	t.Logf("Dart server ready on port %d, peer ID: %s", serverPort, serverPeerIDStr)

	serverPeerID, err := peer.Decode(serverPeerIDStr)
	if err != nil {
		t.Fatalf("decode peer ID: %v", err)
	}

	serverMA, _ := ma.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/udp/%d/udx", serverPort))

	// Create Go host with UDX transport
	client := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Log("Go: connecting to Dart server...")
	err = client.Connect(ctx, peer.AddrInfo{ID: serverPeerID, Addrs: []ma.Multiaddr{serverMA}})
	if err != nil {
		t.Fatal("connect:", err)
	}

	t.Log("Go: opening /echo/1.0.0 stream...")
	s, err := client.NewStream(ctx, serverPeerID, echoProto)
	if err != nil {
		t.Fatal("new stream:", err)
	}

	testData := []byte("hello from Go to Dart over libp2p+UDX+Noise+Yamux!")
	_, err = s.Write(testData)
	if err != nil {
		t.Fatal("write:", err)
	}
	s.CloseWrite()

	buf := make([]byte, 4096)
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal("read echo:", err)
	}
	if string(buf[:n]) != string(testData) {
		t.Fatalf("echo mismatch: got %q, want %q", buf[:n], testData)
	}

	t.Log("Go → Dart libp2p interop PASSED")
}

// TestInteropGoServerDartClient starts a Go libp2p echo server, then launches Dart client.
func TestInteropGoServerDartClient(t *testing.T) {
	server := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer server.Close()

	// Register echo handler
	server.SetStreamHandler(echoProto, func(s network.Stream) {
		defer s.Close()
		io.Copy(s, s)
	})

	addrs := server.Addrs()
	if len(addrs) == 0 {
		t.Fatal("no listen addresses")
	}

	targetAddr := fmt.Sprintf("%s/p2p/%s", addrs[0], server.ID())
	t.Logf("Go server listening, target: %s", targetAddr)

	// Launch Dart client
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dartClient := exec.CommandContext(ctx, "dart", "run", "bin/interop_echo_client.dart", targetAddr)
	dartClient.Dir = dartLibp2pDir

	output, err := dartClient.CombinedOutput()
	t.Logf("[dart-client] %s", string(output))

	if err != nil {
		t.Fatalf("dart client failed: %v", err)
	}

	t.Log("Dart → Go libp2p interop PASSED")
}
