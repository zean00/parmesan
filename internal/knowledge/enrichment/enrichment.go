package enrichment

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/media"
	"github.com/sahal/parmesan/internal/domain/session"
)

type Enricher interface {
	Enrich(ctx context.Context, event session.Event, asset media.Asset, part session.ContentPart) ([]media.DerivedSignal, error)
}

type Noop struct{}

func (Noop) Enrich(context.Context, session.Event, media.Asset, session.ContentPart) ([]media.DerivedSignal, error) {
	return nil, nil
}

type Image struct{}

type Audio struct{}

type PDF struct{}

type Video struct{}

type OpenRouter struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

type Fallback struct {
	Primary  Enricher
	Fallback Enricher
}

func (e Fallback) Enrich(ctx context.Context, event session.Event, asset media.Asset, part session.ContentPart) ([]media.DerivedSignal, error) {
	if e.Primary != nil {
		signals, err := e.Primary.Enrich(ctx, event, asset, part)
		if err == nil && len(signals) > 0 {
			return signals, nil
		}
	}
	if e.Fallback != nil {
		return e.Fallback.Enrich(ctx, event, asset, part)
	}
	return nil, nil
}

func (Image) Enrich(_ context.Context, event session.Event, asset media.Asset, part session.ContentPart) ([]media.DerivedSignal, error) {
	return buildSignals(event, asset, part, "image-heuristic-v1", map[string]string{
		"ocr_text":      metaString(part.Meta, "ocr_text"),
		"image_summary": firstNonEmpty(metaString(part.Meta, "summary"), metaString(part.Meta, "image_summary"), inferSummaryFromURL(part.URL)),
		"image_label":   firstNonEmpty(metaString(part.Meta, "label"), strings.Join(metaStrings(part.Meta, "labels"), ", ")),
	}, map[string]any{"provider": "local", "mode": "heuristic"}), nil
}

func (Audio) Enrich(_ context.Context, event session.Event, asset media.Asset, part session.ContentPart) ([]media.DerivedSignal, error) {
	return buildSignals(event, asset, part, "audio-heuristic-v1", map[string]string{
		"audio_transcript": firstNonEmpty(metaString(part.Meta, "transcript"), metaString(part.Meta, "text")),
		"audio_language":   firstNonEmpty(metaString(part.Meta, "language"), metaString(part.Meta, "lang")),
		"audio_summary":    firstNonEmpty(metaString(part.Meta, "summary"), shortSummary(metaString(part.Meta, "transcript"))),
	}, map[string]any{"provider": "local", "mode": "heuristic"}), nil
}

func (PDF) Enrich(_ context.Context, event session.Event, asset media.Asset, part session.ContentPart) ([]media.DerivedSignal, error) {
	return buildSignals(event, asset, part, "pdf-heuristic-v1", map[string]string{
		"pdf_text":    firstNonEmpty(metaString(part.Meta, "text"), metaString(part.Meta, "pdf_text")),
		"pdf_summary": firstNonEmpty(metaString(part.Meta, "summary"), inferSummaryFromURL(part.URL)),
	}, map[string]any{"provider": "local", "mode": "heuristic"}), nil
}

func (Video) Enrich(_ context.Context, event session.Event, asset media.Asset, part session.ContentPart) ([]media.DerivedSignal, error) {
	return buildSignals(event, asset, part, "video-heuristic-v1", map[string]string{
		"video_summary": firstNonEmpty(metaString(part.Meta, "summary"), inferSummaryFromURL(part.URL)),
		"video_label":   firstNonEmpty(metaString(part.Meta, "label"), strings.Join(metaStrings(part.Meta, "labels"), ", ")),
	}, map[string]any{"provider": "local", "mode": "heuristic"}), nil
}

func (o OpenRouter) Enrich(ctx context.Context, event session.Event, asset media.Asset, part session.ContentPart) ([]media.DerivedSignal, error) {
	if strings.TrimSpace(o.APIKey) == "" {
		return nil, fmt.Errorf("openrouter api key is not configured")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(o.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	modelName := strings.TrimSpace(o.Model)
	if modelName == "" {
		modelName = "openai/gpt-4.1-mini"
	}

	content, err := openRouterContent(part)
	if err != nil {
		return nil, err
	}
	content = append([]map[string]any{{"type": "text", "text": openRouterPrompt(part.Type)}}, content...)
	body := map[string]any{
		"model": modelName,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": content,
			},
		},
		"temperature": 0,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	latencyMS := time.Since(start).Milliseconds()
	requestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	if requestID == "" {
		requestID = strings.TrimSpace(resp.Header.Get("openrouter-request-id"))
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openrouter multimodal failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	text, err := openRouterChoiceText(respBody)
	if err != nil {
		return nil, err
	}
	fields, err := parseSignalJSON(text)
	if err != nil {
		return nil, err
	}
	extractor := "openrouter-" + strings.TrimSpace(part.Type) + "-v1"
	return buildSignals(event, asset, part, extractor, fields, map[string]any{
		"provider":   "openrouter",
		"model":      modelName,
		"mode":       "remote",
		"latency_ms": latencyMS,
		"request_id": requestID,
	}), nil
}

func ForPart(partType string) Enricher {
	var heuristic Enricher = Noop{}
	switch strings.TrimSpace(partType) {
	case "image":
		heuristic = Image{}
	case "audio":
		heuristic = Audio{}
	case "file", "pdf":
		heuristic = PDF{}
	case "video":
		heuristic = Video{}
	default:
		return Noop{}
	}
	primary := OpenRouter{
		BaseURL: strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")),
		APIKey:  strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")),
		Model:   strings.TrimSpace(os.Getenv("OPENROUTER_MULTIMODAL_MODEL")),
	}
	return Fallback{Primary: primary, Fallback: heuristic}
}

func openRouterPrompt(partType string) string {
	switch strings.TrimSpace(partType) {
	case "audio":
		return `Return only compact JSON with keys: audio_transcript, audio_language, audio_summary. Use empty strings when unknown.`
	case "file", "pdf":
		return `Return only compact JSON with keys: pdf_text, pdf_summary. Use empty strings when unknown.`
	case "video":
		return `Return only compact JSON with keys: video_summary, video_label. Use empty strings when unknown.`
	default:
		return `Return only compact JSON with keys: ocr_text, image_summary, image_label. Use empty strings when unknown.`
	}
}

func openRouterContent(part session.ContentPart) ([]map[string]any, error) {
	switch strings.TrimSpace(part.Type) {
	case "image":
		if strings.TrimSpace(part.URL) == "" {
			return nil, fmt.Errorf("image input requires url or data url")
		}
		return []map[string]any{{
			"type": "image_url",
			"image_url": map[string]any{
				"url": strings.TrimSpace(part.URL),
			},
		}}, nil
	case "audio":
		data, format, ok := audioPayload(part)
		if !ok {
			return nil, fmt.Errorf("audio input requires base64 data and format")
		}
		return []map[string]any{{
			"type": "input_audio",
			"input_audio": map[string]any{
				"data":   data,
				"format": format,
			},
		}}, nil
	case "file", "pdf":
		fileData, fileName, ok := pdfPayload(part)
		if !ok {
			return nil, fmt.Errorf("pdf input requires url or base64 data url")
		}
		return []map[string]any{{
			"type": "file",
			"file": map[string]any{
				"filename":  fileName,
				"file_data": fileData,
			},
		}}, nil
	case "video":
		videoData, ok := videoPayload(part)
		if !ok {
			return nil, fmt.Errorf("video input requires url or data url")
		}
		return []map[string]any{{
			"type": "video_url",
			"video_url": map[string]any{
				"url": videoData,
			},
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported multimodal part type %q", part.Type)
	}
}

func audioPayload(part session.ContentPart) (string, string, bool) {
	if encoded := strings.TrimSpace(metaString(part.Meta, "base64")); encoded != "" {
		if !validBase64(encoded) {
			return "", "", false
		}
		return encoded, audioFormat(part), true
	}
	if strings.HasPrefix(strings.TrimSpace(part.URL), "data:audio/") {
		segments := strings.SplitN(part.URL, ",", 2)
		if len(segments) != 2 {
			return "", "", false
		}
		if !validBase64(segments[1]) {
			return "", "", false
		}
		format := audioFormat(part)
		if format == "" {
			format = strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(segments[0], ";", 2)[0], "data:audio/"), ""), "")
		}
		return segments[1], format, true
	}
	return "", "", false
}

func audioFormat(part session.ContentPart) string {
	for _, value := range []string{
		metaString(part.Meta, "format"),
		metaString(part.Meta, "audio_format"),
		strings.TrimPrefix(metaString(part.Meta, "mime_type"), "audio/"),
		strings.TrimPrefix(filepath.Ext(strings.TrimSpace(part.URL)), "."),
	} {
		value = strings.TrimSpace(strings.ToLower(value))
		if value != "" {
			return value
		}
	}
	return ""
}

func pdfPayload(part session.ContentPart) (string, string, bool) {
	if strings.TrimSpace(part.URL) != "" {
		return strings.TrimSpace(part.URL), pdfFileName(part), true
	}
	if encoded := strings.TrimSpace(metaString(part.Meta, "base64")); encoded != "" {
		if !validBase64(encoded) {
			return "", "", false
		}
		return "data:application/pdf;base64," + encoded, pdfFileName(part), true
	}
	return "", "", false
}

func pdfFileName(part session.ContentPart) string {
	if name := strings.TrimSpace(metaString(part.Meta, "filename")); name != "" {
		return name
	}
	if strings.TrimSpace(part.URL) != "" {
		if parsed, err := url.Parse(part.URL); err == nil {
			if base := filepath.Base(parsed.Path); base != "." && base != "/" && base != "" {
				return base
			}
		}
	}
	return "document.pdf"
}

func videoPayload(part session.ContentPart) (string, bool) {
	if raw := strings.TrimSpace(part.URL); raw != "" {
		return raw, true
	}
	if encoded := strings.TrimSpace(metaString(part.Meta, "base64")); encoded != "" {
		if !validBase64(encoded) {
			return "", false
		}
		format := strings.TrimPrefix(strings.TrimSpace(metaString(part.Meta, "mime_type")), "video/")
		if format == "" {
			format = firstNonEmpty(strings.TrimPrefix(strings.TrimSpace(filepath.Ext(metaString(part.Meta, "filename"))), "."), "mp4")
		}
		return "data:video/" + format + ";base64," + encoded, true
	}
	return "", false
}

func openRouterChoiceText(respBody []byte) (string, error) {
	var decoded struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", err
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("openrouter returned no choices")
	}
	switch content := decoded.Choices[0].Message.Content.(type) {
	case string:
		return content, nil
	case []any:
		var parts []string
		for _, item := range content {
			if block, ok := item.(map[string]any); ok {
				if text := strings.TrimSpace(fmt.Sprint(block["text"])); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n"), nil
	default:
		return fmt.Sprint(content), nil
	}
}

func parseSignalJSON(text string) (map[string]string, error) {
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "{"); idx >= 0 {
		text = text[idx:]
	}
	if idx := strings.LastIndex(text, "}"); idx >= 0 {
		text = text[:idx+1]
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for key, value := range raw {
		out[key] = strings.TrimSpace(fmt.Sprint(value))
	}
	return out, nil
}

func buildSignals(event session.Event, asset media.Asset, part session.ContentPart, extractor string, values map[string]string, extraMetadata map[string]any) []media.DerivedSignal {
	now := time.Now().UTC()
	var out []media.DerivedSignal
	for kind, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		metadata := map[string]any{"part_type": part.Type}
		for key, item := range extraMetadata {
			metadata[key] = item
		}
		out = append(out, media.DerivedSignal{
			ID:         fmt.Sprintf("signal_%s_%s", asset.ID, kind),
			AssetID:    asset.ID,
			SessionID:  event.SessionID,
			EventID:    event.ID,
			Kind:       kind,
			Value:      value,
			Confidence: 0.7,
			Metadata:   metadata,
			Extractor:  extractor,
			CreatedAt:  now,
		})
	}
	return out
}

func metaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if value, ok := meta[key]; ok {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

func metaStrings(meta map[string]any, key string) []string {
	if meta == nil {
		return nil
	}
	value, ok := meta[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" {
			return nil
		}
		return strings.Split(text, ",")
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func inferSummaryFromURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := filepath.Base(parsed.Path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.ReplaceAll(base, "_", " ")
	base = strings.ReplaceAll(base, "-", " ")
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	return "Image related to " + base
}

func shortSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if idx := strings.IndexAny(text, ".!?"); idx > 0 {
		return strings.TrimSpace(text[:idx+1])
	}
	if len(text) > 140 {
		return strings.TrimSpace(text[:140]) + "..."
	}
	return text
}

func validBase64(data string) bool {
	if strings.TrimSpace(data) == "" {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(data)
	return err == nil
}
