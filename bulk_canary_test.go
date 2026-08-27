package udxtransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// Bulk canary for the UDX transport under the real libp2p stack.
//
// TestHostEcho proves a connection can be established and 35 bytes survive it.
// That is not enough to catch the failure this file exists for: go-udx stalled
// permanently at ~256KB per stream (go-udx doc/TRANSPORT_WINDOW_BUG.md), and
// every test in this repo moved so little data that none of them noticed. The
// bug reached production and came back as a bug report.
//
// This is deliberately the *transport* half of go-ricochet's TestVaultSyncBatched,
// which pushes ~1MB of documents over one connection and was the downstream
// canary that did catch it. That test needs PostgreSQL and lives in another
// repo; the property it guards does not need either. It cannot live in go-udx
// itself, which would be a module cycle (go-udx -> go-libp2p-udx-transport ->
// go-udx), so this repo is the right home: it already depends on both go-udx
// and libp2p.
//
// What matters is that megabytes cross Noise + yamux over a single UDX stream,
// which is exactly how this transport is used — transport.go opens one UDX
// stream per connection and lets the upgrader multiplex above it.

const (
	bulkProto  = "/udx-canary/bulk/1.0.0"
	batchProto = "/udx-canary/batch/1.0.0"
)

// patternByte generates deterministic payload bytes, matching the generator in
// go-udx's own transfer tests so a corruption offset means the same thing in
// both repos.
func patternByte(i int) byte { return byte(i*31 + i/251) }

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = patternByte(i)
	}
	return b
}

// markedPayload stamps a marker over every 512th byte so a receiver can tell
// which stream a payload came from. Byte counts alone cannot catch data
// delivered intact to the wrong stream.
func markedPayload(n, marker int) []byte {
	b := payload(n)
	for i := 0; i < n; i += 512 {
		b[i] = byte(marker)
	}
	return b
}

// digestReply is the fixed-size reply the bulk handler sends once it has read
// to EOF: the byte count it saw, then the SHA-256 of those bytes. Verifying a
// digest rather than echoing the payload back keeps the assertion exact without
// doubling the traffic.
const digestReplyLen = 8 + sha256.Size

func bulkHandler(s network.Stream) {
	defer s.Close()
	h := sha256.New()
	n, err := io.Copy(h, s)
	if err != nil {
		s.Reset()
		return
	}
	var reply [digestReplyLen]byte
	binary.BigEndian.PutUint64(reply[:8], uint64(n))
	copy(reply[8:], h.Sum(nil))
	if _, err := s.Write(reply[:]); err != nil {
		s.Reset()
	}
}

// TestBulkTransferOverUDX is the direct regression for the ~256KB stall: push
// far more than that through one libp2p stream and verify every byte arrived.
func TestBulkTransferOverUDX(t *testing.T) {
	const total = 4 << 20 // 16x the old ceiling

	server := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer server.Close()
	server.SetStreamHandler(bulkProto, bulkHandler)

	client := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatal("connect:", err)
	}

	body := payload(total)
	got, err := bulkRoundTrip(ctx, client, server.ID(), bulkProto, body, 90*time.Second)
	if err != nil {
		t.Fatalf("%d-byte transfer: %v", total, err)
	}
	if got.count != uint64(total) {
		t.Fatalf("receiver saw %d of %d bytes", got.count, total)
	}
	want := sha256.Sum256(body)
	if !bytes.Equal(got.digest, want[:]) {
		t.Fatal("payload corrupted in transit: digests differ")
	}
	t.Logf("%d bytes through Noise + yamux over UDX, verified", total)
}

// TestBatchedRequestResponseOverUDX mirrors the shape of the downstream vault
// sync: a handful of large request/response round trips rather than one long
// push. Interleaving reads and writes on a stream exercises paths a one-way
// transfer never reaches — a sender that stalls waiting for credit it already
// has shows up here and not in the bulk test.
func TestBatchedRequestResponseOverUDX(t *testing.T) {
	// 5 batches x 100 documents x 2KB, the vault-sync workload.
	const batches = 5
	const perBatch = 100 * 2048

	server := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer server.Close()
	server.SetStreamHandler(batchProto, func(s network.Stream) {
		defer s.Close()
		var hdr [4]byte
		for {
			if _, err := io.ReadFull(s, hdr[:]); err != nil {
				return // clean EOF when the client is done
			}
			body := make([]byte, binary.BigEndian.Uint32(hdr[:]))
			if _, err := io.ReadFull(s, body); err != nil {
				s.Reset()
				return
			}
			sum := sha256.Sum256(body)
			if _, err := s.Write(sum[:]); err != nil {
				s.Reset()
				return
			}
		}
	})

	client := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatal("connect:", err)
	}

	s, err := client.NewStream(ctx, server.ID(), batchProto)
	if err != nil {
		t.Fatal("new stream:", err)
	}
	defer s.Close()
	if err := s.SetDeadline(time.Now().Add(90 * time.Second)); err != nil {
		t.Fatal(err)
	}

	body := payload(perBatch)
	want := sha256.Sum256(body)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))

	for i := 0; i < batches; i++ {
		if _, err := s.Write(hdr[:]); err != nil {
			t.Fatalf("batch %d header: %v", i, err)
		}
		if _, err := s.Write(body); err != nil {
			t.Fatalf("batch %d body: %v", i, err)
		}
		var sum [sha256.Size]byte
		if _, err := io.ReadFull(s, sum[:]); err != nil {
			t.Fatalf("batch %d reply (stalled after %d of %d batches): %v",
				i, i, batches, err)
		}
		if !bytes.Equal(sum[:], want[:]) {
			t.Fatalf("batch %d: server received different bytes than were sent", i)
		}
	}
	t.Logf("%d round trips of %d bytes each, verified", batches, perBatch)
}

// TestConcurrentStreamsOverUDX runs several libp2p streams at once over the one
// UDX stream this transport opens per connection, which is how yamux is
// actually used here. Each carries a distinct marker so misrouted data fails
// rather than merely arriving.
func TestConcurrentStreamsOverUDX(t *testing.T) {
	const streams = 4
	const perStream = 512 << 10

	server := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer server.Close()
	server.SetStreamHandler(bulkProto, bulkHandler)

	client := makeHost(t, "/ip4/127.0.0.1/udp/0/udx")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	if err := client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatal("connect:", err)
	}

	errs := make(chan error, streams)
	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func(marker int) {
			defer wg.Done()
			body := markedPayload(perStream, marker)
			got, err := bulkRoundTrip(ctx, client, server.ID(), bulkProto, body, 120*time.Second)
			if err != nil {
				errs <- fmt.Errorf("stream %d: %w", marker, err)
				return
			}
			if got.count != uint64(perStream) {
				errs <- fmt.Errorf("stream %d: receiver saw %d of %d bytes",
					marker, got.count, perStream)
				return
			}
			want := sha256.Sum256(body)
			if !bytes.Equal(got.digest, want[:]) {
				errs <- fmt.Errorf("stream %d: payload does not match what was sent "+
					"— data was corrupted or crossed between streams", marker)
			}
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(150 * time.Second):
		t.Fatal("concurrent stream transfer hung")
	}

	close(errs)
	failed := false
	for err := range errs {
		t.Error(err)
		failed = true
	}
	if !failed {
		t.Logf("%d concurrent streams x %d bytes, verified per stream", streams, perStream)
	}
}

type bulkResult struct {
	count  uint64
	digest []byte
}

// bulkRoundTrip opens a stream, sends body, half-closes, and reads back the
// receiver's count and digest.
func bulkRoundTrip(
	ctx context.Context,
	client host.Host,
	srv peer.ID,
	proto protocol.ID,
	body []byte,
	timeout time.Duration,
) (bulkResult, error) {
	s, err := client.NewStream(ctx, srv, proto)
	if err != nil {
		return bulkResult{}, fmt.Errorf("new stream: %w", err)
	}
	defer s.Close()
	if err := s.SetDeadline(time.Now().Add(timeout)); err != nil {
		return bulkResult{}, err
	}

	if n, err := s.Write(body); err != nil {
		return bulkResult{}, fmt.Errorf("stalled after writing %d of %d bytes: %w",
			n, len(body), err)
	}
	if err := s.CloseWrite(); err != nil {
		return bulkResult{}, fmt.Errorf("close write: %w", err)
	}

	var reply [digestReplyLen]byte
	if _, err := io.ReadFull(s, reply[:]); err != nil {
		return bulkResult{}, fmt.Errorf("reading receipt: %w", err)
	}
	return bulkResult{
		count:  binary.BigEndian.Uint64(reply[:8]),
		digest: reply[8:],
	}, nil
}
