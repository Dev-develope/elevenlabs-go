package sixtydb_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haguro/elevenlabs-go/sixtydb"
)

const (
	mockAPIKey  = "sk_live_MockAPIKey"
	mockTimeout = 10 * time.Second
)

type testServerConfig struct {
	keyOptional    bool
	expectedMethod string
	expectedPath   string
	statusCode     int
	responseBody   []byte
}

func testServer(t *testing.T, config testServerConfig) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !config.keyOptional {
			gotAuth := r.Header.Get("Authorization")
			wantAuth := "Bearer " + mockAPIKey
			if gotAuth != wantAuth {
				t.Errorf("Server: expected Authorization %q, got %q", wantAuth, gotAuth)
			}
		}
		if config.expectedMethod != "" && r.Method != config.expectedMethod {
			t.Errorf("Server: expected method %q, got %q", config.expectedMethod, r.Method)
		}
		if config.expectedPath != "" && r.URL.Path != config.expectedPath {
			t.Errorf("Server: expected path %q, got %q", config.expectedPath, r.URL.Path)
		}
		w.WriteHeader(config.statusCode)
		w.Write(config.responseBody)
	}))
}

// newClient returns a client pointed at the test server. The 60db Client has no
// exported base-URL setter, so we mutate it via a small NewClient + reflection
// alternative: instead we rely on the exported helper below.
func newClient(ctx context.Context, baseURL string) *sixtydb.Client {
	return sixtydb.NewTestClient(ctx, baseURL, baseURL, mockAPIKey, mockTimeout)
}

func TestTextToSpeech(t *testing.T) {
	t.Parallel()
	wantAudio := []byte("fake-mp3-bytes")
	respBody, _ := json.Marshal(sixtydb.SynthesizeResponse{
		Success:    true,
		Audio:      base64.StdEncoding.EncodeToString(wantAudio),
		SampleRate: 44100,
		Duration:   1.23,
		Encoding:   "mp3",
	})
	server := testServer(t, testServerConfig{
		expectedMethod: http.MethodPost,
		expectedPath:   "/tts-synthesize",
		statusCode:     http.StatusOK,
		responseBody:   respBody,
	})
	defer server.Close()

	client := newClient(context.Background(), server.URL)
	got, err := client.TextToSpeech("voice-123", sixtydb.TextToSpeechRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, wantAudio) {
		t.Errorf("expected audio %q, got %q", wantAudio, got)
	}
}

func TestSynthesizeMetadata(t *testing.T) {
	t.Parallel()
	respBody, _ := json.Marshal(sixtydb.SynthesizeResponse{
		Success:    true,
		Audio:      base64.StdEncoding.EncodeToString([]byte("x")),
		SampleRate: 24000,
		Duration:   2.5,
		Encoding:   "wav",
	})
	server := testServer(t, testServerConfig{
		expectedMethod: http.MethodPost,
		expectedPath:   "/tts-synthesize",
		statusCode:     http.StatusOK,
		responseBody:   respBody,
	})
	defer server.Close()

	client := newClient(context.Background(), server.URL)
	resp, err := client.Synthesize("voice-123", sixtydb.TextToSpeechRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.SampleRate != 24000 || resp.Duration != 2.5 || resp.Encoding != "wav" {
		t.Errorf("unexpected metadata: %+v", resp)
	}
}

func TestTextToSpeechRequestBody(t *testing.T) {
	t.Parallel()
	var gotBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		resp, _ := json.Marshal(sixtydb.SynthesizeResponse{Success: true, Audio: base64.StdEncoding.EncodeToString([]byte("a"))})
		w.WriteHeader(http.StatusOK)
		w.Write(resp)
	}))
	defer server.Close()

	client := newClient(context.Background(), server.URL)
	_, err := client.TextToSpeech("voice-from-arg", sixtydb.TextToSpeechRequest{Text: "hi", VoiceID: "should-be-overwritten"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["voice_id"] != "voice-from-arg" {
		t.Errorf("expected voice_id from method arg to win, got %v", gotBody["voice_id"])
	}
	if gotBody["text"] != "hi" {
		t.Errorf("expected text %q, got %v", "hi", gotBody["text"])
	}
}

func TestTextToSpeechAPIError(t *testing.T) {
	t.Parallel()
	server := testServer(t, testServerConfig{
		expectedMethod: http.MethodPost,
		statusCode:     http.StatusUnauthorized,
		responseBody:   []byte(`{"success":false,"message":"invalid api key"}`),
	})
	defer server.Close()

	client := newClient(context.Background(), server.URL)
	_, err := client.TextToSpeech("voice-123", sixtydb.TextToSpeechRequest{Text: "hello"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var apiErr *sixtydb.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *sixtydb.APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Error(), "invalid api key") {
		t.Errorf("expected error to contain message, got %q", apiErr.Error())
	}
}

func TestTextToSpeechUnsuccessful(t *testing.T) {
	t.Parallel()
	server := testServer(t, testServerConfig{
		expectedMethod: http.MethodPost,
		statusCode:     http.StatusOK,
		responseBody:   []byte(`{"success":false,"message":"synthesis failed"}`),
	})
	defer server.Close()

	client := newClient(context.Background(), server.URL)
	_, err := client.TextToSpeech("voice-123", sixtydb.TextToSpeechRequest{Text: "hello"})
	if err == nil {
		t.Fatal("expected an error for success=false, got nil")
	}
}

func TestTextToSpeechStream(t *testing.T) {
	t.Parallel()
	chunks := [][]byte{[]byte("part-one-"), []byte("part-two-"), []byte("part-three")}
	var sb strings.Builder
	for _, c := range chunks {
		frame, _ := json.Marshal(map[string]string{"type": "chunk", "data": base64.StdEncoding.EncodeToString(c)})
		sb.Write(frame)
		sb.WriteByte('\n')
	}
	complete, _ := json.Marshal(map[string]string{"type": "complete"})
	sb.Write(complete)
	sb.WriteByte('\n')

	server := testServer(t, testServerConfig{
		expectedMethod: http.MethodPost,
		expectedPath:   "/tts-stream",
		statusCode:     http.StatusOK,
		responseBody:   []byte(sb.String()),
	})
	defer server.Close()

	client := newClient(context.Background(), server.URL)
	var out bytes.Buffer
	if err := client.TextToSpeechStream(&out, "voice-123", sixtydb.TextToSpeechRequest{Text: "hello"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "part-one-part-two-part-three"
	if out.String() != want {
		t.Errorf("expected %q, got %q", want, out.String())
	}
}

func TestTextToSpeechStreamErrorFrame(t *testing.T) {
	t.Parallel()
	frame, _ := json.Marshal(map[string]string{"type": "error", "error": "boom"})
	server := testServer(t, testServerConfig{
		expectedMethod: http.MethodPost,
		statusCode:     http.StatusOK,
		responseBody:   append(frame, '\n'),
	})
	defer server.Close()

	client := newClient(context.Background(), server.URL)
	err := client.TextToSpeechStream(&bytes.Buffer{}, "voice-123", sixtydb.TextToSpeechRequest{Text: "hello"})
	if err == nil {
		t.Fatal("expected error from error frame, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected error to contain %q, got %q", "boom", err.Error())
	}
}

func TestRequestTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := sixtydb.NewTestClient(context.Background(), server.URL, server.URL, mockAPIKey, 50*time.Millisecond)
	_, err := client.TextToSpeech("voice-123", sixtydb.TextToSpeechRequest{Text: "hello"})
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
}

func ExampleClient_TextToSpeech() {
	sixtydb.SetAPIKey("your-api-key")
	audio, err := sixtydb.TextToSpeech("voice-id", sixtydb.TextToSpeechRequest{
		Text: "Hello from 60db!",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	_ = audio // os.WriteFile("hello.mp3", audio, 0644)
}
