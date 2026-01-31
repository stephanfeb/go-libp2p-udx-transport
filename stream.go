package udxtransport

import (
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	udx "github.com/stephanfeb/go-udx"
)

// stream wraps a udx.Stream to implement network.MuxedStream.
type stream struct {
	udxStream *udx.Stream
}

var _ network.MuxedStream = (*stream)(nil)

func (s *stream) Read(p []byte) (int, error) {
	return s.udxStream.Read(p)
}

func (s *stream) Write(p []byte) (int, error) {
	return s.udxStream.Write(p)
}

func (s *stream) Close() error {
	return s.udxStream.Close()
}

func (s *stream) CloseRead() error {
	// UDX doesn't have a separate CloseRead — deliver a reset to stop reading
	return nil
}

func (s *stream) CloseWrite() error {
	return s.udxStream.CloseWrite()
}

func (s *stream) Reset() error {
	return s.udxStream.Reset(udx.ErrorInternalError)
}

func (s *stream) ResetWithError(errCode network.StreamErrorCode) error {
	return s.udxStream.Reset(uint32(errCode))
}

func (s *stream) SetDeadline(t time.Time) error {
	if err := s.udxStream.SetReadDeadline(t); err != nil {
		return err
	}
	return s.udxStream.SetWriteDeadline(t)
}

func (s *stream) SetReadDeadline(t time.Time) error {
	return s.udxStream.SetReadDeadline(t)
}

func (s *stream) SetWriteDeadline(t time.Time) error {
	return s.udxStream.SetWriteDeadline(t)
}
