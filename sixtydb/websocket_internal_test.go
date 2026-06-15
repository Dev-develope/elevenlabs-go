package sixtydb

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// fakeConn is a minimal net.Conn backed by an independent read source and write
// sink, used to exercise the frame codec without real sockets.
type fakeConn struct {
	r io.Reader
	w io.Writer
}

func (c *fakeConn) Read(b []byte) (int, error)       { return c.r.Read(b) }
func (c *fakeConn) Write(b []byte) (int, error)      { return c.w.Write(b) }
func (c *fakeConn) Close() error                     { return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return nil }
func (c *fakeConn) RemoteAddr() net.Addr             { return nil }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

// frameBytes returns the wire bytes writeFrame produces for the given opcode and
// payload (masked, as a client frame).
func frameBytes(t *testing.T, opcode byte, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := &wsConn{conn: &fakeConn{w: &buf}}
	if err := w.writeFrame(opcode, payload); err != nil {
		t.Fatalf("writeFrame error: %v", err)
	}
	return buf.Bytes()
}

func newReader(b []byte) *wsConn {
	src := bytes.NewReader(b)
	conn := &fakeConn{r: src, w: io.Discard}
	return &wsConn{conn: conn, br: bufio.NewReader(conn)}
}

// TestComputeAcceptKey uses the example key/accept pair from RFC 6455 §1.3.
func TestComputeAcceptKey(t *testing.T) {
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	const want = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got := computeAcceptKey(key); got != want {
		t.Errorf("computeAcceptKey(%q) = %q, want %q", key, got, want)
	}
}

// TestFrameRoundTrip writes masked frames of various sizes and reads them back,
// verifying framing, length encoding and (un)masking across the 7-bit, 16-bit
// and 64-bit length boundaries.
func TestFrameRoundTrip(t *testing.T) {
	sizes := []int{0, 5, 125, 126, 200, 0xFFFF, 0x10000}
	for _, n := range sizes {
		payload := bytes.Repeat([]byte{0xAB}, n)
		reader := newReader(frameBytes(t, opText, payload))

		fin, opcode, got, err := reader.readFrame()
		if err != nil {
			t.Fatalf("size %d: readFrame error: %v", n, err)
		}
		if !fin {
			t.Errorf("size %d: expected FIN set", n)
		}
		if opcode != opText {
			t.Errorf("size %d: expected opcode 0x%x, got 0x%x", n, opText, opcode)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("size %d: payload round-trip mismatch", n)
		}
	}
}

// TestReadMessageHandlesPing verifies a ping control frame is transparently
// handled (answered with a pong via the write sink) and does not interrupt
// assembly of the following text message.
func TestReadMessageHandlesPing(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(frameBytes(t, opPing, []byte("hb")))
	stream.Write(frameBytes(t, opText, []byte("hello")))

	reader := newReader(stream.Bytes())
	opcode, payload, err := reader.readMessage()
	if err != nil {
		t.Fatalf("readMessage error: %v", err)
	}
	if opcode != opText || string(payload) != "hello" {
		t.Errorf("expected text \"hello\", got opcode 0x%x payload %q", opcode, payload)
	}
}

// TestReadMessageFragmented verifies a fragmented message (text + continuation)
// is reassembled.
func TestReadMessageFragmented(t *testing.T) {
	// Build two unmasked fragments by hand: first text frame with FIN=0, then a
	// continuation frame with FIN=1.
	var stream bytes.Buffer
	stream.Write([]byte{opText, 0x03})             // FIN=0, opcode=text, len=3
	stream.WriteString("foo")
	stream.Write([]byte{0x80 | opContinuation, 0x03}) // FIN=1, continuation, len=3
	stream.WriteString("bar")

	reader := newReader(stream.Bytes())
	opcode, payload, err := reader.readMessage()
	if err != nil {
		t.Fatalf("readMessage error: %v", err)
	}
	if opcode != opText || string(payload) != "foobar" {
		t.Errorf("expected \"foobar\", got opcode 0x%x payload %q", opcode, payload)
	}
}

// TestReadMessageClose verifies a close frame surfaces as io.EOF.
func TestReadMessageClose(t *testing.T) {
	reader := newReader(frameBytes(t, opClose, nil))
	if _, _, err := reader.readMessage(); err != io.EOF {
		t.Errorf("expected io.EOF on close frame, got %v", err)
	}
}
