package udxtransport

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
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
	// The server stops when its stdin closes. Keep stdin open until the
	// test ends; without a pipe, stdin is /dev/null and the server stops
	// as soon as it is ready.
	stdin, err := dartServer.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()

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

const reqRespProto = "/interop/reqresp/1.0.0"

// TestInteropDartRequestResponse checks that a Dart client's request
// reaches a Go server when the client writes it and closes the stream after
// the response, as a Ricochet client does. The server reads one
// length-prefixed frame per stream, as the go-ricochet pipeline does, and
// counts the streams that end before a whole frame arrives. The server uses
// go-ricochet's yamux keepalive settings.
func TestInteropDartRequestResponse(t *testing.T) {
	ymx := *yamux.DefaultTransport
	ymx.KeepAliveInterval = 15 * time.Second
	ymx.ConnectionWriteTimeout = 10 * time.Second
	server, err := libp2p.New(
		libp2p.NoTransports,
		libp2p.Transport(NewTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Muxer(yamux.ID, &ymx),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/udx"),
		libp2p.ResourceManager(&network.NullResourceManager{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	var served, short atomic.Int32
	server.SetStreamHandler(reqRespProto, func(s network.Stream) {
		defer s.Close()
		s.SetReadDeadline(time.Now().Add(10 * time.Second))
		var lenBuf [4]byte
		if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
			short.Add(1)
			t.Logf("stream ended before the length prefix: %v", err)
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(lenBuf[:]))
		if _, err := io.ReadFull(s, body); err != nil {
			short.Add(1)
			t.Logf("stream ended inside the frame: %v", err)
			return
		}
		var req struct{ N int }
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request %q: %v", body, err)
			return
		}
		resp := []byte(fmt.Sprintf("ok %d", req.N))
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(resp)))
		s.Write(lenBuf[:])
		s.Write(resp)
		served.Add(1)
	})

	target := fmt.Sprintf("%s/p2p/%s", server.Addrs()[0], server.ID())
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const count = 40
	client := exec.CommandContext(ctx, "dart", "run", "bin/interop_reqresp_client.dart", target, strconv.Itoa(count))
	client.Dir = dartLibp2pDir
	output, err := client.CombinedOutput()
	t.Logf("[dart-client] %s", output)
	if err != nil {
		t.Fatalf("dart client failed: %v", err)
	}
	if n := short.Load(); n > 0 {
		t.Fatalf("%d streams ended before the server read a whole request", n)
	}
	if n := served.Load(); n != count {
		t.Fatalf("server answered %d requests, want %d", n, count)
	}
}
