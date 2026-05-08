package session

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const NexusLocationContentType = "application/vnd.nexus.location+json"

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name,omitempty"`
	Address   string  `json:"address,omitempty"`
}

func LocationText(location Location) string {
	label := strings.TrimSpace(location.Name)
	if label == "" {
		label = "Location"
	}
	coords := fmt.Sprintf("%.6f, %.6f", location.Latitude, location.Longitude)
	link := fmt.Sprintf("https://maps.google.com/?q=%.6f,%.6f", location.Latitude, location.Longitude)
	if address := strings.TrimSpace(location.Address); address != "" {
		return fmt.Sprintf("%s: %s\n%s\n%s", label, coords, address, link)
	}
	return fmt.Sprintf("%s: %s\n%s", label, coords, link)
}

func LocationFromContentPart(part ContentPart) (Location, bool) {
	if !IsLocationContentPart(part) {
		return Location{}, false
	}
	for _, data := range []map[string]any{part.Data, part.Meta} {
		if location, ok := locationFromMap(data); ok {
			return location, true
		}
	}
	payload := strings.TrimSpace(part.Content)
	if payload == "" {
		payload = strings.TrimSpace(part.Text)
	}
	if payload != "" {
		var data map[string]any
		if err := json.Unmarshal([]byte(payload), &data); err == nil {
			if location, ok := locationFromMap(data); ok {
				return location, true
			}
		}
	}
	return Location{}, false
}

func IsLocationContentPart(part ContentPart) bool {
	for _, value := range []string{
		part.Type,
		part.ContentType,
		part.MimeType,
		stringValue(part.Data, "content_type"),
		stringValue(part.Data, "mime_type"),
		stringValue(part.Meta, "content_type"),
		stringValue(part.Meta, "mime_type"),
	} {
		if strings.EqualFold(strings.TrimSpace(value), NexusLocationContentType) {
			return true
		}
	}
	kind := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		stringValue(part.Data, "kind"),
		stringValue(part.Meta, "kind"),
	)))
	switch kind {
	case "location", "geo_location", "geolocation", "coordinate", "coordinates":
		return true
	default:
		return false
	}
}

func locationFromMap(data map[string]any) (Location, bool) {
	if len(data) == 0 {
		return Location{}, false
	}
	if nested, ok := anyMap(data["location"]); ok {
		if location, ok := locationFromMap(nested); ok {
			return location, true
		}
	}
	lat, okLat := numberValue(data, "latitude", "lat")
	lon, okLon := numberValue(data, "longitude", "lon", "lng")
	if !okLat || !okLon || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return Location{}, false
	}
	return Location{
		Latitude:  lat,
		Longitude: lon,
		Name:      firstNonEmpty(stringValue(data, "name"), stringValue(data, "title"), stringValue(data, "label")),
		Address:   stringValue(data, "address"),
	}, true
}

func numberValue(data map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		value, ok := data[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return typed, true
		case float32:
			return float64(typed), true
		case int:
			return float64(typed), true
		case int64:
			return float64(typed), true
		case json.Number:
			n, err := typed.Float64()
			return n, err == nil
		case string:
			n, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
			return n, err == nil
		}
	}
	return 0, false
}

func stringValue(data map[string]any, key string) string {
	if len(data) == 0 {
		return ""
	}
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func anyMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = value
		}
		return out, true
	default:
		return nil, false
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
