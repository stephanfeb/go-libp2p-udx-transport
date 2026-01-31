# go-libp2p-udx-transport

A [go-libp2p](https://github.com/libp2p/go-libp2p) transport implementation using [UDX](../go-udx) — a QUIC-inspired reliable UDP protocol with built-in stream multiplexing.

## Overview

This package implements the go-libp2p `Transport` interface, allowing libp2p hosts to communicate over UDX. Like the QUIC transport, UDX has native stream multiplexing so this transport provides `CapableConn` directly without needing an Upgrader or separate muxer.

## Multiaddr Format

```
/ip4/<addr>/udp/<port>/udx
/ip6/<addr>/udp/<port>/udx
```

Protocol code: `0x0300`

## Installation

```
go get github.com/stephanfeb/go-libp2p-udx-transport
```

## Usage

```go
package main

import (
    "context"
    "fmt"

    ic "github.com/libp2p/go-libp2p/core/crypto"
    "github.com/libp2p/go-libp2p/core/peer"
    ma "github.com/multiformats/go-multiaddr"
    udxtransport "github.com/stephanfeb/go-libp2p-udx-transport"
)

func main() {
    // Generate identity
    priv, _, _ := ic.GenerateEd25519Key(nil)

    // Create transport
    tr, _ := udxtransport.NewTransport(priv, nil)

    // Listen
    addr, _ := ma.NewMultiaddr("/ip4/0.0.0.0/udp/9000/udx")
    ln, _ := tr.Listen(addr)
    defer ln.Close()

    fmt.Println("Listening on", ln.Multiaddr())

    // Accept connections
    conn, _ := ln.Accept()
    stream, _ := conn.AcceptStream()

    buf := make([]byte, 1024)
    n, _ := stream.Read(buf)
    fmt.Printf("received: %s\n", buf[:n])
}
```

## Architecture

```
transport.go    Transport — Dial, Listen, CanDial, Protocols, Proxy
conn.go         CapableConn wrapping udx.Connection
stream.go       MuxedStream wrapping udx.Stream
listener.go     Listener wrapping udx.Multiplexer
multiaddr.go    /udx protocol registration (0x0300), multiaddr helpers
```

### Interface Mapping

| libp2p Interface | UDX Implementation |
|-----------------|-------------------|
| `transport.Transport` | `Transport` — manages identity and resource manager |
| `transport.CapableConn` | `conn` — wraps `udx.Connection` |
| `transport.Listener` | `listener` — wraps `udx.Multiplexer` |
| `network.MuxedStream` | `stream` — wraps `udx.Stream` |

## Testing

```bash
go test -race ./...
```

Tests cover:
- `CanDial` — multiaddr filtering (`/udx` vs `/tcp`)
- `Protocols` — protocol code advertisement
- `Proxy` — non-proxy declaration
- `ListenAndDial` — full loopback: listen, dial, open stream, bidirectional echo
- `Multiaddr` — round-trip multiaddr construction and parsing

## Dependencies

- [go-udx](../go-udx) — UDX protocol implementation
- [go-libp2p/core](https://github.com/libp2p/go-libp2p) — libp2p interfaces
- [go-multiaddr](https://github.com/multiformats/go-multiaddr) — multiaddr encoding

## License

MIT
