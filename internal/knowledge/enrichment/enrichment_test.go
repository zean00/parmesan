package enrichment

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/media"
	"github.com/sahal/parmesan/internal/domain/session"
)

func TestOpenRouterImageEnrichBuildsImageURLRequest(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "req_image_1")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ocr_text\":\"ORDER-123\",\"image_summary\":\"Cracked screen\",\"image_label\":\"phone damage\"}"}}]}`))
	}))
	defer srv.Close()

	signals, err := OpenRouter{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "openai/gpt-4.1-mini",
		Client:  srv.Client(),
	}.Enrich(context.Background(), session.Event{
		ID: "evt_1", SessionID: "sess_1",
	}, media.Asset{
		ID: "asset_1", SessionID: "sess_1", EventID: "evt_1", CreatedAt: time.Now().UTC(),
	}, session.ContentPart{
		Type: "image",
		URL:  "https://example.test/damage.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 3 {
		t.Fatalf("signals = %#v, want 3 image signals", signals)
	}
	if signals[0].Metadata["provider"] != "openrouter" || signals[0].Metadata["request_id"] != "req_image_1" {
		t.Fatalf("signal metadata = %#v, want openrouter request metadata", signals[0].Metadata)
	}
	messages := captured["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	image := content[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("request content = %#v, want image_url block", image)
	}
}

func TestOpenRouterAudioEnrichBuildsInputAudioRequest(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "req_audio_1")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"audio_transcript\":\"Please email me updates\",\"audio_language\":\"en\",\"audio_summary\":\"Customer requested email updates.\"}"}}]}`))
	}))
	defer srv.Close()

	audioBase64 := base64.StdEncoding.EncodeToString([]byte("fake-audio"))
	signals, err := OpenRouter{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "openai/gpt-4.1-mini",
		Client:  srv.Client(),
	}.Enrich(context.Background(), session.Event{
		ID: "evt_1", SessionID: "sess_1",
	}, media.Asset{
		ID: "asset_1", SessionID: "sess_1", EventID: "evt_1", CreatedAt: time.Now().UTC(),
	}, session.ContentPart{
		Type: "audio",
		Meta: map[string]any{
			"base64": audioBase64,
			"format": "wav",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 3 {
		t.Fatalf("signals = %#v, want 3 audio signals", signals)
	}
	messages := captured["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	audio := content[1].(map[string]any)
	if audio["type"] != "input_audio" {
		t.Fatalf("request content = %#v, want input_audio block", audio)
	}
}

func TestOpenRouterPDFEnrichBuildsFileRequest(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"pdf_text\":\"Return policy\",\"pdf_summary\":\"Refund instructions\"}"}}]}`))
	}))
	defer srv.Close()

	signals, err := OpenRouter{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Client:  srv.Client(),
	}.Enrich(context.Background(), session.Event{
		ID: "evt_1", SessionID: "sess_1",
	}, media.Asset{
		ID: "asset_1", SessionID: "sess_1", EventID: "evt_1", CreatedAt: time.Now().UTC(),
	}, session.ContentPart{
		Type: "file",
		URL:  "https://example.test/policy.pdf",
		Meta: map[string]any{"filename": "policy.pdf"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 2 {
		t.Fatalf("signals = %#v, want 2 pdf signals", signals)
	}
	messages := captured["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	file := content[1].(map[string]any)
	if file["type"] != "file" {
		t.Fatalf("request content = %#v, want file block", file)
	}
}

func TestOpenRouterVideoEnrichBuildsVideoURLRequest(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"video_summary\":\"Customer shows damaged package\",\"video_label\":\"damage evidence\"}"}}]}`))
	}))
	defer srv.Close()

	signals, err := OpenRouter{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Client:  srv.Client(),
	}.Enrich(context.Background(), session.Event{
		ID: "evt_1", SessionID: "sess_1",
	}, media.Asset{
		ID: "asset_1", SessionID: "sess_1", EventID: "evt_1", CreatedAt: time.Now().UTC(),
	}, session.ContentPart{
		Type: "video",
		URL:  "https://example.test/evidence.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 2 {
		t.Fatalf("signals = %#v, want 2 video signals", signals)
	}
	messages := captured["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	video := content[1].(map[string]any)
	if video["type"] != "video_url" {
		t.Fatalf("request content = %#v, want video_url block", video)
	}
}
