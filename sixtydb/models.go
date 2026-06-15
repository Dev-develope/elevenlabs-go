package sixtydb

import "encoding/base64"

// Output formats accepted by the /tts-synthesize endpoint's "output_format"
// field.
const (
	OutputFormatMP3  = "mp3"
	OutputFormatWAV  = "wav"
	OutputFormatOGG  = "ogg"
	OutputFormatFLAC = "flac"
)

// WebSocket audio encodings accepted by the "audio_config.encoding" field.
const (
	EncodingLinear16 = "LINEAR16" // Raw PCM, 16-bit signed little-endian, mono.
	EncodingMulaw    = "MULAW"    // μ-law, 8kHz.
	EncodingOggOpus  = "OGG_OPUS" // Ogg Opus, 24kHz.
)

// TextToSpeechRequest is the request body for the /tts-synthesize and
// /tts-stream endpoints.
//
// Only Text is required. The remaining fields are sent only when set (non-zero),
// allowing the server's documented defaults to apply: Speed 1, Stability 50,
// Similarity 75, Enhance true, OutputFormat "mp3". Use the pointer fields
// (Stability, Similarity, Enhance) when you need to send an explicit zero/false
// value rather than relying on the default.
//
// VoiceID is populated from the voiceID argument of the client methods; setting
// it on the struct directly is optional and will be overwritten by that
// argument.
type TextToSpeechRequest struct {
	Text         string  `json:"text"`
	VoiceID      string  `json:"voice_id,omitempty"`
	Speed        float64 `json:"speed,omitempty"`
	Stability    *int    `json:"stability,omitempty"`
	Similarity   *int    `json:"similarity,omitempty"`
	Enhance      *bool   `json:"enhance,omitempty"`
	OutputFormat string  `json:"output_format,omitempty"`
}

// SynthesizeResponse is the JSON payload returned by the /tts-synthesize
// endpoint. Audio holds the base64-encoded audio as received; use AudioBytes to
// obtain the decoded audio.
type SynthesizeResponse struct {
	Success    bool    `json:"success"`
	Message    string  `json:"message"`
	Audio      string  `json:"audio"`
	SampleRate int     `json:"sample_rate"`
	Duration   float64 `json:"duration"`
	Encoding   string  `json:"encoding"`
}

// AudioBytes decodes and returns the base64-encoded audio in the response.
func (r *SynthesizeResponse) AudioBytes() ([]byte, error) {
	return base64.StdEncoding.DecodeString(r.Audio)
}

// streamFrame is a single newline-delimited JSON frame from the /tts-stream
// endpoint. The documented frame types are "chunk", "complete" and "error".
//
// The exact field carrying the base64 audio is not pinned down in the public
// docs, so several plausible names are accepted defensively; audioData returns
// whichever is populated.
type streamFrame struct {
	Type    string `json:"type"`
	Data    string `json:"data,omitempty"`
	Audio   string `json:"audio,omitempty"`
	Chunk   string `json:"chunk,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (f *streamFrame) audioData() string {
	switch {
	case f.Data != "":
		return f.Data
	case f.Audio != "":
		return f.Audio
	default:
		return f.Chunk
	}
}

func (f *streamFrame) errorText() string {
	if f.Error != "" {
		return f.Error
	}
	return f.Message
}
