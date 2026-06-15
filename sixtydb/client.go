// Package sixtydb provides an interface to interact with the 60db
// (https://60db.ai) text-to-speech API in Go.
//
// It is a sibling to the elevenlabs package and deliberately mirrors its shape:
// a Client value carrying an API key, timeout and parent context, a configurable
// default client used by package-level shorthand functions, and methods that
// return raw audio bytes (TextToSpeech) or stream audio into an io.Writer
// (TextToSpeechStream). A WebSocket interface is also provided (see
// websocket.go). This makes the two providers easy to use side by side; the
// elevenlabs client remains the primary/default provider for the repository.
//
// Authentication uses a bearer token (Authorization: Bearer <key>) for the REST
// endpoints and an apiKey query parameter for the WebSocket endpoint.
package sixtydb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sixtydbBaseURL  = "https://api.60db.ai"
	sixtydbWSURL    = "wss://api.60db.ai/ws/tts"
	defaultTimeout  = 30 * time.Second
	contentTypeJSON = "application/json"
)

var (
	once          sync.Once
	defaultClient *Client
)

// Client represents an API client that can be used to make calls to the 60db
// API. The NewClient function should be used when instantiating a new Client.
//
// This library also includes a default client instance that can be used when
// it's more convenient or when only a single instance of Client will ever be
// used by the program. The default client's API key and timeout (which defaults
// to 30 seconds) can be modified with SetAPIKey and SetTimeout respectively, but
// the parent context is fixed and is set to context.Background().
type Client struct {
	baseURL string
	wsURL   string
	apiKey  string
	timeout time.Duration
	ctx     context.Context
}

func getDefaultClient() *Client {
	once.Do(func() {
		defaultClient = NewClient(context.Background(), "", defaultTimeout)
	})
	return defaultClient
}

// SetAPIKey sets the API key for the default client.
//
// It should be called before making any API calls with the default client.
func SetAPIKey(apiKey string) {
	getDefaultClient().apiKey = apiKey
}

// SetTimeout sets the timeout duration for the default client.
func SetTimeout(timeout time.Duration) {
	getDefaultClient().timeout = timeout
}

// NewClient creates and returns a new Client object with the provided settings.
//
// It takes a context.Context that acts as the parent context for requests made
// by this client, the API key to be used for authenticated requests and a
// timeout duration for the client's requests.
func NewClient(ctx context.Context, apiKey string, reqTimeout time.Duration) *Client {
	return &Client{
		baseURL: sixtydbBaseURL,
		wsURL:   sixtydbWSURL,
		apiKey:  apiKey,
		timeout: reqTimeout,
		ctx:     ctx,
	}
}

// newRequest builds an authenticated *http.Request for the 60db REST API.
func (c *Client) newRequest(ctx context.Context, method, url string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

// apiErrorFromResponse reads a non-2xx response body and converts it into an
// *APIError, falling back to the raw body text when it cannot be decoded.
func apiErrorFromResponse(resp *http.Response) error {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	apiErr := &APIError{StatusCode: resp.StatusCode}
	if jsonErr := json.Unmarshal(respBody, apiErr); jsonErr == nil && (apiErr.Message != "" || apiErr.Error_ != "") {
		return apiErr
	}
	apiErr.Message = strings.TrimSpace(string(respBody))
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(resp.StatusCode)
	}
	return apiErr
}

// Synthesize converts the given text to speech and returns the full decoded
// response, including audio metadata (sample rate, duration, encoding).
//
// The voiceID argument selects the voice and overrides any VoiceID set on the
// request. It returns an error if the request fails or the server reports an
// unsuccessful synthesis.
func (c *Client) Synthesize(voiceID string, ttsReq TextToSpeechRequest) (*SynthesizeResponse, error) {
	ttsReq.VoiceID = voiceID
	reqBody, err := json.Marshal(ttsReq)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodPost, c.baseURL+"/tts-synthesize", bytes.NewReader(reqBody), contentTypeJSON)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, apiErrorFromResponse(resp)
	}

	var sr SynthesizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	if !sr.Success {
		return nil, &APIError{StatusCode: resp.StatusCode, Message: sr.Message}
	}
	return &sr, nil
}

// TextToSpeech converts and returns a given text to speech audio using a certain
// voice.
//
// It takes a voiceID and a TextToSpeechRequest, and returns the decoded audio
// bytes (in the requested output format, MP3 by default) in case of success, or
// an error. This mirrors elevenlabs.Client.TextToSpeech so the two providers can
// be swapped behind a common call site.
func (c *Client) TextToSpeech(voiceID string, ttsReq TextToSpeechRequest) ([]byte, error) {
	sr, err := c.Synthesize(voiceID, ttsReq)
	if err != nil {
		return nil, err
	}
	return sr.AudioBytes()
}

// TextToSpeechStream converts and streams a given text to speech audio using a
// certain voice, copying the decoded audio to the provided io.Writer as it
// arrives.
//
// The /tts-stream endpoint delivers newline-delimited JSON frames; this method
// decodes the base64 audio carried by each "chunk" frame and writes the raw
// audio bytes to streamWriter, returning when a "complete" frame is received.
// An "error" frame is surfaced as an *APIError.
//
// It is important to set the client timeout to a duration large enough to
// maintain the desired streaming period. It returns nil if successful or an
// error otherwise.
func (c *Client) TextToSpeechStream(streamWriter io.Writer, voiceID string, ttsReq TextToSpeechRequest) error {
	ttsReq.VoiceID = voiceID
	reqBody, err := json.Marshal(ttsReq)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodPost, c.baseURL+"/tts-stream", bytes.NewReader(reqBody), contentTypeJSON)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return apiErrorFromResponse(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	// Audio chunks are base64 and can be large; raise the per-line limit well
	// above bufio's 64KiB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var frame streamFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			return fmt.Errorf("sixtydb: failed to decode stream frame: %w", err)
		}
		switch frame.Type {
		case "chunk":
			audio, err := base64.StdEncoding.DecodeString(frame.audioData())
			if err != nil {
				return fmt.Errorf("sixtydb: failed to decode audio chunk: %w", err)
			}
			if _, err := streamWriter.Write(audio); err != nil {
				return err
			}
		case "complete":
			return nil
		case "error":
			return &APIError{Message: frame.errorText()}
		}
	}
	return scanner.Err()
}
