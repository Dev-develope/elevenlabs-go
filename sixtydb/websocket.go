package sixtydb

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WebSocket opcodes (RFC 6455 §5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// wsMagicGUID is the fixed GUID used to compute the Sec-WebSocket-Accept value
// (RFC 6455 §1.3).
const wsMagicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// maxWSFrameSize caps the payload size of a single inbound frame to guard
// against a malformed/hostile length header.
const maxWSFrameSize = 64 << 20 // 64 MiB

// WebSocketConfig configures a 60db WebSocket TTS context.
//
// Only VoiceID is required. Encoding defaults to LINEAR16 and SampleRate to
// 24000 when left zero. ContextID is generated automatically when empty.
type WebSocketConfig struct {
	VoiceID    string
	Encoding   string // one of EncodingLinear16, EncodingMulaw, EncodingOggOpus
	SampleRate int
	Speed      float64
	Stability  *int
	Similarity *int
	ContextID  string
}

// WSMessage is a server -> client message received over the WebSocket. The
// documented Type values include "connection_established", "context_created",
// "audio_chunk", "flush_completed", "context_closed" and "error". Raw holds the
// undecoded JSON for access to any fields not modeled here.
type WSMessage struct {
	Type         string          `json:"type"`
	ContextID    string          `json:"context_id,omitempty"`
	AudioContent string          `json:"audioContent,omitempty"`
	Message      string          `json:"message,omitempty"`
	Error        string          `json:"error,omitempty"`
	Raw          json.RawMessage `json:"-"`
}

// Audio decodes and returns the base64-encoded audio carried by an
// "audio_chunk" message.
func (m WSMessage) Audio() ([]byte, error) {
	return base64.StdEncoding.DecodeString(m.AudioContent)
}

func (m WSMessage) errorText() string {
	if m.Error != "" {
		return m.Error
	}
	return m.Message
}

// Client -> server message envelopes.
type wsAudioConfig struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sample_rate"`
}

type wsCreateContext struct {
	Type        string        `json:"type"`
	ContextID   string        `json:"context_id"`
	VoiceID     string        `json:"voice_id"`
	AudioConfig wsAudioConfig `json:"audio_config"`
	Speed       float64       `json:"speed,omitempty"`
	Stability   *int          `json:"stability,omitempty"`
	Similarity  *int          `json:"similarity,omitempty"`
}

type wsSendText struct {
	Type      string `json:"type"`
	ContextID string `json:"context_id"`
	Text      string `json:"text"`
}

type wsContextOp struct {
	Type      string `json:"type"`
	ContextID string `json:"context_id"`
}

// wsConn is a minimal RFC 6455 WebSocket connection (client side) built on the
// standard library so the package adds no third-party dependencies. It supports
// the small subset of the protocol needed by the 60db TTS API: masked text
// frames out, (possibly fragmented) frames in, and ping/pong/close control
// frames.
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex // serializes writes (control frames may interleave)
}

// WSConn is an open WebSocket session to the 60db TTS endpoint. Use the
// CreateContext / SendText / FlushContext / CloseContext methods to drive the
// protocol and ReadMessage to consume server messages. Call Close when done.
//
// For the common case, prefer the high-level Client.TextToSpeechWebSocket which
// performs the full connect -> create -> send -> flush -> receive -> close
// sequence for you.
type WSConn struct {
	c  *Client
	ws *wsConn
}

// DialWebSocket opens a WebSocket connection to the 60db TTS endpoint and
// consumes the initial "connection_established" message.
//
// The connection's deadline is derived from ctx (if it carries one) or from the
// client timeout otherwise; set a large timeout when streaming long-form audio.
func (c *Client) DialWebSocket(ctx context.Context) (*WSConn, error) {
	u, err := url.Parse(c.wsURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if c.apiKey != "" {
		q.Set("apiKey", c.apiKey)
	}
	u.RawQuery = q.Encode()

	secure := u.Scheme == "wss"
	port := u.Port()
	if port == "" {
		if secure {
			port = "443"
		} else {
			port = "80"
		}
	}
	address := net.JoinHostPort(u.Hostname(), port)

	dialer := &net.Dialer{}
	netConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}

	var conn net.Conn = netConn
	if secure {
		tlsConn := tls.Client(netConn, &tls.Config{ServerName: u.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			netConn.Close()
			return nil, err
		}
		conn = tlsConn
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.timeout)
	}
	_ = conn.SetDeadline(deadline)

	ws := &wsConn{conn: conn, br: bufio.NewReader(conn)}
	if err := ws.handshake(u); err != nil {
		conn.Close()
		return nil, err
	}

	wc := &WSConn{c: c, ws: ws}
	// The server emits "connection_established" immediately after the upgrade.
	msg, err := wc.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if msg.Type == "error" {
		conn.Close()
		return nil, &APIError{Message: msg.errorText()}
	}
	return wc, nil
}

func (ws *wsConn) handshake(u *url.URL) error {
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	var sb strings.Builder
	fmt.Fprintf(&sb, "GET %s HTTP/1.1\r\n", u.RequestURI())
	fmt.Fprintf(&sb, "Host: %s\r\n", u.Host)
	sb.WriteString("Upgrade: websocket\r\n")
	sb.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&sb, "Sec-WebSocket-Key: %s\r\n", key)
	sb.WriteString("Sec-WebSocket-Version: 13\r\n\r\n")
	if _, err := ws.conn.Write([]byte(sb.String())); err != nil {
		return err
	}

	resp, err := http.ReadResponse(ws.br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("sixtydb: websocket handshake failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != computeAcceptKey(key) {
		return errors.New("sixtydb: websocket handshake failed: invalid Sec-WebSocket-Accept")
	}
	return nil
}

func computeAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(wsMagicGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// writeFrame writes a single masked frame. All client-to-server frames must be
// masked (RFC 6455 §5.3).
func (ws *wsConn) writeFrame(opcode byte, payload []byte) error {
	ws.wmu.Lock()
	defer ws.wmu.Unlock()

	var header [14]byte
	header[0] = 0x80 | opcode // FIN set, single frame
	n := len(payload)
	var hlen int
	switch {
	case n <= 125:
		header[1] = 0x80 | byte(n)
		hlen = 2
	case n <= 0xFFFF:
		header[1] = 0x80 | 126
		binary.BigEndian.PutUint16(header[2:4], uint16(n))
		hlen = 4
	default:
		header[1] = 0x80 | 127
		binary.BigEndian.PutUint64(header[2:10], uint64(n))
		hlen = 10
	}

	var maskKey [4]byte
	if _, err := rand.Read(maskKey[:]); err != nil {
		return err
	}
	copy(header[hlen:hlen+4], maskKey[:])
	hlen += 4

	if _, err := ws.conn.Write(header[:hlen]); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ maskKey[i%4]
	}
	_, err := ws.conn.Write(masked)
	return err
}

// readFrame reads a single frame, unmasking the payload if necessary.
func (ws *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(ws.br, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	opcode = h[0] & 0x0f
	masked := h[1]&0x80 != 0
	length := uint64(h[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(ws.br, ext[:]); err != nil {
			return
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(ws.br, ext[:]); err != nil {
			return
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > maxWSFrameSize {
		err = fmt.Errorf("sixtydb: inbound websocket frame too large (%d bytes)", length)
		return
	}
	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(ws.br, maskKey[:]); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(ws.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return
}

// readMessage reads a full application message, transparently handling
// fragmentation and ping/pong/close control frames. A close frame surfaces as
// io.EOF.
func (ws *wsConn) readMessage() (opcode byte, payload []byte, err error) {
	var data []byte
	var msgOpcode byte
	started := false
	for {
		fin, op, p, e := ws.readFrame()
		if e != nil {
			return 0, nil, e
		}
		switch op {
		case opPing:
			if e := ws.writeFrame(opPong, p); e != nil {
				return 0, nil, e
			}
			continue
		case opPong:
			continue
		case opClose:
			_ = ws.writeFrame(opClose, nil)
			return 0, nil, io.EOF
		case opContinuation:
			if !started {
				return 0, nil, errors.New("sixtydb: unexpected continuation frame")
			}
			data = append(data, p...)
		case opText, opBinary:
			if started {
				return 0, nil, errors.New("sixtydb: new data frame before previous was finished")
			}
			msgOpcode = op
			started = true
			data = append(data, p...)
		default:
			return 0, nil, fmt.Errorf("sixtydb: unknown websocket opcode 0x%x", op)
		}
		if fin {
			return msgOpcode, data, nil
		}
	}
}

// ReadMessage reads and decodes the next server message.
func (wc *WSConn) ReadMessage() (WSMessage, error) {
	_, payload, err := wc.ws.readMessage()
	if err != nil {
		return WSMessage{}, err
	}
	var m WSMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return WSMessage{}, fmt.Errorf("sixtydb: failed to decode websocket message: %w", err)
	}
	m.Raw = append(json.RawMessage(nil), payload...)
	return m, nil
}

func (wc *WSConn) send(v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return wc.ws.writeFrame(opText, b)
}

// CreateContext sends a "create_context" message, returning the context ID used
// (generated when cfg.ContextID is empty).
func (wc *WSConn) CreateContext(cfg WebSocketConfig) (string, error) {
	contextID := cfg.ContextID
	if contextID == "" {
		id, err := randomID()
		if err != nil {
			return "", err
		}
		contextID = id
	}
	encoding := cfg.Encoding
	if encoding == "" {
		encoding = EncodingLinear16
	}
	sampleRate := cfg.SampleRate
	if sampleRate == 0 {
		sampleRate = 24000
	}
	msg := wsCreateContext{
		Type:        "create_context",
		ContextID:   contextID,
		VoiceID:     cfg.VoiceID,
		AudioConfig: wsAudioConfig{Encoding: encoding, SampleRate: sampleRate},
		Speed:       cfg.Speed,
		Stability:   cfg.Stability,
		Similarity:  cfg.Similarity,
	}
	if err := wc.send(msg); err != nil {
		return "", err
	}
	return contextID, nil
}

// SendText appends text to the context's buffer via a "send_text" message.
func (wc *WSConn) SendText(contextID, text string) error {
	return wc.send(wsSendText{Type: "send_text", ContextID: contextID, Text: text})
}

// FlushContext triggers synthesis of the buffered text via "flush_context".
func (wc *WSConn) FlushContext(contextID string) error {
	return wc.send(wsContextOp{Type: "flush_context", ContextID: contextID})
}

// CloseContext finalizes the context via "close_context".
func (wc *WSConn) CloseContext(contextID string) error {
	return wc.send(wsContextOp{Type: "close_context", ContextID: contextID})
}

// Close sends a WebSocket close frame (best effort) and closes the underlying
// connection.
func (wc *WSConn) Close() error {
	_ = wc.ws.writeFrame(opClose, nil)
	return wc.ws.conn.Close()
}

// TextToSpeechWebSocket synthesizes text over the WebSocket API and copies the
// decoded audio to w as "audio_chunk" messages arrive, returning once the
// "flush_completed" message is received.
//
// It performs the full lifecycle (connect, create context, send text, flush,
// receive, close) and is the recommended entry point for single-shot WebSocket
// synthesis. For incremental/interactive streaming, use DialWebSocket and drive
// the connection directly.
//
// Set a large client timeout when synthesizing long-form audio, as the timeout
// bounds the whole session.
func (c *Client) TextToSpeechWebSocket(w io.Writer, text string, cfg WebSocketConfig) error {
	conn, err := c.DialWebSocket(c.ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	contextID, err := conn.CreateContext(cfg)
	if err != nil {
		return err
	}
	if err := conn.SendText(contextID, text); err != nil {
		return err
	}
	if err := conn.FlushContext(contextID); err != nil {
		return err
	}

	for {
		msg, err := conn.ReadMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch msg.Type {
		case "audio_chunk":
			audio, err := msg.Audio()
			if err != nil {
				return fmt.Errorf("sixtydb: failed to decode audio chunk: %w", err)
			}
			if _, err := w.Write(audio); err != nil {
				return err
			}
		case "flush_completed":
			_ = conn.CloseContext(contextID)
			return nil
		case "error":
			return &APIError{Message: msg.errorText()}
		}
	}
}

// randomID returns a 16-character hex identifier suitable for a context ID.
func randomID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
