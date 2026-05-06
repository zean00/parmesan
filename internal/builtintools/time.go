package builtintools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/tool"
)

const (
	ProviderID        = "builtin"
	CurrentTimeToolID = "builtin_current_time"
	CurrentTimeName   = "get_current_time"
)

type Repository interface {
	RegisterProvider(ctx context.Context, binding tool.ProviderBinding) error
	SaveCatalogEntries(ctx context.Context, entries []tool.CatalogEntry) error
}

func Provider(now time.Time) tool.ProviderBinding {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return tool.ProviderBinding{
		ID:           ProviderID,
		Kind:         tool.ProviderNative,
		Name:         "Built-in utilities",
		RegisteredAt: now,
		Healthy:      true,
	}
}

func CatalogEntries(now time.Time) []tool.CatalogEntry {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return []tool.CatalogEntry{{
		ID:              CurrentTimeToolID,
		ProviderID:      ProviderID,
		Name:            CurrentTimeName,
		Description:     "Get the current local date and time for an IANA timezone or known location. Use this for customer-facing questions about current time, local date, weekday, UTC offset, or timezone-aware scheduling context.",
		Schema:          currentTimeSchema(),
		RuntimeProtocol: "native",
		MetadataJSON: mustJSON(map[string]any{
			"source":        "builtin",
			"builtin":       true,
			"auto_expose":   true,
			"consequential": false,
		}),
		ImportedAt: now,
	}}
}

func Ensure(ctx context.Context, repo Repository) error {
	now := time.Now().UTC()
	if err := repo.RegisterProvider(ctx, Provider(now)); err != nil {
		return fmt.Errorf("register built-in provider: %w", err)
	}
	if err := repo.SaveCatalogEntries(ctx, CatalogEntries(now)); err != nil {
		return fmt.Errorf("save built-in catalog: %w", err)
	}
	return nil
}

func Invoke(entry tool.CatalogEntry, input map[string]any, now time.Time) (map[string]any, error) {
	switch strings.TrimSpace(entry.Name) {
	case CurrentTimeName:
		return currentTime(input, now)
	default:
		return nil, fmt.Errorf("unsupported built-in tool %q", entry.Name)
	}
}

func currentTime(input map[string]any, now time.Time) (map[string]any, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	timezone := stringArg(input, "timezone")
	location := stringArg(input, "location")
	loc, canonicalLocation, err := resolveLocation(timezone, location)
	if err != nil {
		return nil, err
	}
	local := now.In(loc)
	zoneName, offsetSeconds := local.Zone()
	return map[string]any{
		"utc":                now.UTC().Format(time.RFC3339),
		"local_time":         local.Format(time.RFC3339),
		"local_date":         local.Format("2006-01-02"),
		"local_clock":        local.Format("15:04:05"),
		"weekday":            local.Weekday().String(),
		"timezone":           loc.String(),
		"timezone_name":      zoneName,
		"utc_offset":         formatUTCOffset(offsetSeconds),
		"utc_offset_seconds": offsetSeconds,
		"location":           canonicalLocation,
	}, nil
}

func resolveLocation(timezone, location string) (*time.Location, string, error) {
	timezone = strings.TrimSpace(timezone)
	location = strings.TrimSpace(location)
	if timezone != "" {
		loc, err := loadTimezone(timezone)
		if err != nil {
			return nil, "", err
		}
		return loc, location, nil
	}
	if location != "" {
		if tz, ok := locationTimezone(location); ok {
			loc, err := loadTimezone(tz)
			if err != nil {
				return nil, "", err
			}
			return loc, canonicalLocationName(location), nil
		}
		return nil, "", fmt.Errorf("unknown location %q; provide an IANA timezone such as Asia/Jakarta", location)
	}
	return time.UTC, "", nil
}

func loadTimezone(value string) (*time.Location, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.UTC, nil
	}
	if loc, err := time.LoadLocation(value); err == nil {
		return loc, nil
	}
	if loc, ok := parseUTCOffset(value); ok {
		return loc, nil
	}
	return nil, fmt.Errorf("unknown timezone %q; use an IANA timezone such as Asia/Jakarta or an offset such as UTC+07:00", value)
}

func parseUTCOffset(value string) (*time.Location, bool) {
	upper := strings.ToUpper(strings.TrimSpace(value))
	upper = strings.TrimPrefix(upper, "GMT")
	upper = strings.TrimPrefix(upper, "UTC")
	if upper == "" || upper == "Z" {
		return time.UTC, true
	}
	sign := 1
	switch upper[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return nil, false
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(upper, "+"), "-"), ":")
	if len(parts) == 0 || len(parts) > 2 {
		return nil, false
	}
	hours, err := parseTwoDigitInt(parts[0])
	if err != nil || hours > 14 {
		return nil, false
	}
	minutes := 0
	if len(parts) == 2 {
		minutes, err = parseTwoDigitInt(parts[1])
		if err != nil || minutes > 59 {
			return nil, false
		}
	}
	offset := sign * ((hours * 3600) + (minutes * 60))
	return time.FixedZone(fmt.Sprintf("UTC%s", formatUTCOffset(offset)), offset), true
}

func parseTwoDigitInt(value string) (int, error) {
	if value == "" || len(value) > 2 {
		return 0, errors.New("invalid integer")
	}
	out := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("invalid integer")
		}
		out = out*10 + int(r-'0')
	}
	return out, nil
}

func formatUTCOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	return fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
}

func locationTimezone(value string) (string, bool) {
	key := normalizeLocation(value)
	tz, ok := locationTimezones()[key]
	return tz, ok
}

func canonicalLocationName(value string) string {
	key := normalizeLocation(value)
	if key == "" {
		return strings.TrimSpace(value)
	}
	return titleWords(key)
}

func titleWords(value string) string {
	parts := strings.Fields(value)
	for i, part := range parts {
		if len(part) == 0 {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func normalizeLocation(value string) string {
	replacer := strings.NewReplacer(",", " ", ".", " ", "_", " ", "-", " ")
	return strings.Join(strings.Fields(strings.ToLower(replacer.Replace(strings.TrimSpace(value)))), " ")
}

func locationTimezones() map[string]string {
	return map[string]string{
		"jakarta":       "Asia/Jakarta",
		"indonesia":     "Asia/Jakarta",
		"singapore":     "Asia/Singapore",
		"kuala lumpur":  "Asia/Kuala_Lumpur",
		"bangkok":       "Asia/Bangkok",
		"tokyo":         "Asia/Tokyo",
		"seoul":         "Asia/Seoul",
		"shanghai":      "Asia/Shanghai",
		"beijing":       "Asia/Shanghai",
		"hong kong":     "Asia/Hong_Kong",
		"sydney":        "Australia/Sydney",
		"melbourne":     "Australia/Melbourne",
		"london":        "Europe/London",
		"paris":         "Europe/Paris",
		"berlin":        "Europe/Berlin",
		"amsterdam":     "Europe/Amsterdam",
		"new york":      "America/New_York",
		"nyc":           "America/New_York",
		"san francisco": "America/Los_Angeles",
		"los angeles":   "America/Los_Angeles",
		"seattle":       "America/Los_Angeles",
		"chicago":       "America/Chicago",
		"denver":        "America/Denver",
		"toronto":       "America/Toronto",
		"vancouver":     "America/Vancouver",
		"mexico city":   "America/Mexico_City",
		"sao paulo":     "America/Sao_Paulo",
		"buenos aires":  "America/Argentina/Buenos_Aires",
		"dubai":         "Asia/Dubai",
		"riyadh":        "Asia/Riyadh",
		"mumbai":        "Asia/Kolkata",
		"delhi":         "Asia/Kolkata",
		"johannesburg":  "Africa/Johannesburg",
		"lagos":         "Africa/Lagos",
		"nairobi":       "Africa/Nairobi",
	}
}

func stringArg(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	value, ok := input[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func currentTimeSchema() string {
	return mustJSON(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "IANA timezone such as Asia/Jakarta or America/New_York. UTC offsets such as UTC+07:00 are also accepted.",
			},
			"location": map[string]any{
				"type":        "string",
				"description": "Customer or business location, such as Jakarta, London, New York, or San Francisco. Used only for built-in local timezone aliases.",
			},
			"locale": map[string]any{
				"type":        "string",
				"description": "Optional BCP 47 locale hint for the caller. The tool returns deterministic ISO-like fields regardless of locale.",
			},
		},
	})
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `{}`
	}
	return string(raw)
}
