package sixtydb

import (
	"context"
	"io"
)

// This file provides package-level shorthand functions that proxy to the
// default client, mirroring the elevenlabs package's generated shorthand.go.
// It is maintained by hand (this package is not wired into cmd/codegen).

// Synthesize calls the Synthesize method on the default client.
func Synthesize(voiceID string, ttsReq TextToSpeechRequest) (*SynthesizeResponse, error) {
	return getDefaultClient().Synthesize(voiceID, ttsReq)
}

// TextToSpeech calls the TextToSpeech method on the default client.
func TextToSpeech(voiceID string, ttsReq TextToSpeechRequest) ([]byte, error) {
	return getDefaultClient().TextToSpeech(voiceID, ttsReq)
}

// TextToSpeechStream calls the TextToSpeechStream method on the default client.
func TextToSpeechStream(streamWriter io.Writer, voiceID string, ttsReq TextToSpeechRequest) error {
	return getDefaultClient().TextToSpeechStream(streamWriter, voiceID, ttsReq)
}

// TextToSpeechWebSocket calls the TextToSpeechWebSocket method on the default client.
func TextToSpeechWebSocket(w io.Writer, text string, cfg WebSocketConfig) error {
	return getDefaultClient().TextToSpeechWebSocket(w, text, cfg)
}

// DialWebSocket calls the DialWebSocket method on the default client.
func DialWebSocket(ctx context.Context) (*WSConn, error) {
	return getDefaultClient().DialWebSocket(ctx)
}
