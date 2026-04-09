package sessionwatch

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store"
)

const (
	KindDeliveryStatus      = "delivery_status"
	KindAppointmentReminder = "appointment_reminder"

	SourceRuntime   = "runtime"
	SourceLifecycle = "lifecycle"
)

type UpdateIntent struct {
	Kind          string
	Source        string
	SubjectRef    string
	ToolID        string
	Arguments     map[string]any
	PollInterval  time.Duration
	NextRunAt     time.Time
	StopCondition string
	DedupeKey     string
}

func EnsureSessionWatch(ctx context.Context, repo store.Repository, sess session.Session, intent UpdateIntent, now time.Time) (session.Watch, bool, error) {
	intent = NormalizeIntent(intent, now)
	if strings.TrimSpace(intent.Kind) == "" || strings.TrimSpace(sess.ID) == "" {
		return session.Watch{}, false, nil
	}
	watches, err := repo.ListSessionWatches(ctx, session.WatchQuery{SessionID: sess.ID})
	if err != nil {
		return session.Watch{}, false, err
	}
	for _, item := range watches {
		if !EquivalentWatch(item, intent) {
			continue
		}
		if item.Status != session.WatchStatusFailed {
			return item, false, nil
		}
	}
	watch := session.Watch{
		ID:            fmt.Sprintf("swatch_%d", now.UnixNano()),
		SessionID:     sess.ID,
		Kind:          intent.Kind,
		Status:        session.WatchStatusActive,
		Source:        intent.Source,
		SubjectRef:    intent.SubjectRef,
		ToolID:        intent.ToolID,
		Arguments:     cloneMap(intent.Arguments),
		PollInterval:  intent.PollInterval,
		NextRunAt:     intent.NextRunAt,
		StopCondition: strings.TrimSpace(intent.StopCondition),
		DedupeKey:     intent.DedupeKey,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := repo.SaveSessionWatch(ctx, watch); err != nil {
		return session.Watch{}, false, err
	}
	return watch, true, nil
}

func NormalizeIntent(intent UpdateIntent, now time.Time) UpdateIntent {
	intent.Kind = strings.TrimSpace(intent.Kind)
	intent.Source = firstNonEmpty(intent.Source, SourceLifecycle)
	intent.SubjectRef = strings.TrimSpace(intent.SubjectRef)
	intent.ToolID = strings.TrimSpace(intent.ToolID)
	if intent.PollInterval < 0 {
		intent.PollInterval = 0
	}
	if intent.Kind == KindDeliveryStatus && intent.PollInterval <= 0 {
		intent.PollInterval = 15 * time.Minute
	}
	if intent.DedupeKey == "" {
		intent.DedupeKey = BuildDedupeKey(intent)
	}
	if intent.NextRunAt.IsZero() {
		if intent.Kind == KindDeliveryStatus && intent.PollInterval > 0 {
			intent.NextRunAt = now.Add(intent.PollInterval)
		}
	}
	if intent.Kind == KindAppointmentReminder && intent.NextRunAt.Before(now.Add(5*time.Second)) {
		intent.NextRunAt = now.Add(5 * time.Second)
	}
	return intent
}

func EquivalentWatch(item session.Watch, intent UpdateIntent) bool {
	intent = NormalizeIntent(intent, time.Now().UTC())
	if strings.TrimSpace(item.DedupeKey) != "" && strings.TrimSpace(intent.DedupeKey) != "" {
		return strings.TrimSpace(item.DedupeKey) == strings.TrimSpace(intent.DedupeKey)
	}
	return strings.TrimSpace(item.Kind) == strings.TrimSpace(intent.Kind) &&
		strings.TrimSpace(item.SubjectRef) == strings.TrimSpace(intent.SubjectRef) &&
		strings.TrimSpace(item.ToolID) == strings.TrimSpace(intent.ToolID)
}

func BuildDedupeKey(intent UpdateIntent) string {
	sum := sha1.Sum([]byte(strings.Join([]string{
		strings.ToLower(strings.TrimSpace(intent.Kind)),
		strings.ToLower(strings.TrimSpace(intent.SubjectRef)),
		strings.ToLower(strings.TrimSpace(intent.ToolID)),
	}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func BuildDeliveryIntent(source, toolID, subjectRef string, args map[string]any, now time.Time) (UpdateIntent, bool) {
	subjectRef = strings.TrimSpace(subjectRef)
	toolID = strings.TrimSpace(toolID)
	if subjectRef == "" || toolID == "" {
		return UpdateIntent{}, false
	}
	return NormalizeIntent(UpdateIntent{
		Kind:          KindDeliveryStatus,
		Source:        source,
		SubjectRef:    subjectRef,
		ToolID:        toolID,
		Arguments:     cloneMap(args),
		PollInterval:  15 * time.Minute,
		StopCondition: "delivered",
	}, now), true
}

func BuildAppointmentReminderIntent(source, subjectRef string, remindAt time.Time, args map[string]any, now time.Time) (UpdateIntent, bool) {
	if remindAt.IsZero() {
		return UpdateIntent{}, false
	}
	if subjectRef == "" {
		subjectRef = remindAt.UTC().Format(time.RFC3339)
	}
	return NormalizeIntent(UpdateIntent{
		Kind:       KindAppointmentReminder,
		Source:     source,
		SubjectRef: subjectRef,
		Arguments:  cloneMap(args),
		NextRunAt:  remindAt.UTC(),
	}, now), true
}

func ReminderTimeFromAppointment(appointmentAt, now time.Time) time.Time {
	if appointmentAt.IsZero() {
		return time.Time{}
	}
	remindAt := appointmentAt.Add(-1 * time.Hour)
	if remindAt.Before(now.Add(5 * time.Second)) {
		remindAt = now.Add(5 * time.Second)
	}
	return remindAt.UTC()
}

func ExtractSubjectRef(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(fmt.Sprint(args[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func ParseAppointmentTime(args map[string]any, now time.Time) (time.Time, bool) {
	if len(args) == 0 {
		return time.Time{}, false
	}
	keys := []string{"remind_at", "appointment_at", "starts_at", "scheduled_for", "date", "datetime", "time"}
	for _, key := range keys {
		if parsed, ok := parseTimeValue(args[key], now); ok {
			return parsed, true
		}
	}
	dateText := strings.TrimSpace(fmt.Sprint(args["date"]))
	timeText := strings.TrimSpace(fmt.Sprint(args["time"]))
	if dateText != "" && timeText != "" && dateText != "<nil>" && timeText != "<nil>" {
		if parsed, ok := parseDateAndTime(dateText, timeText, now); ok {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func ParseAppointmentTimeFromText(text string, now time.Time) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, false
	}
	lower := strings.ToLower(text)
	tomorrowAt := regexp.MustCompile(`tomorrow at ([0-9]{1,2}(?::[0-9]{2})?\s*(?:am|pm)?)`)
	if matches := tomorrowAt.FindStringSubmatch(lower); len(matches) == 2 {
		return parseTimeString("tomorrow at "+strings.TrimSpace(matches[1]), now)
	}
	isoAt := regexp.MustCompile(`([0-9]{4}-[0-9]{2}-[0-9]{2})(?:[ t]+([0-9]{1,2}(?::[0-9]{2})?\s*(?:am|pm)?))`)
	if matches := isoAt.FindStringSubmatch(lower); len(matches) == 3 {
		return parseDateAndTime(matches[1], matches[2], now)
	}
	isoDate := regexp.MustCompile(`\b([0-9]{4}-[0-9]{2}-[0-9]{2})\b`)
	if matches := isoDate.FindStringSubmatch(lower); len(matches) == 2 {
		if parsed, err := time.Parse("2006-01-02", matches[1]); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func parseTimeValue(raw any, now time.Time) (time.Time, bool) {
	switch value := raw.(type) {
	case time.Time:
		if value.IsZero() {
			return time.Time{}, false
		}
		return value.UTC(), true
	case string:
		return parseTimeString(value, now)
	default:
		return time.Time{}, false
	}
}

func parseTimeString(value string, now time.Time) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "<nil>" {
		return time.Time{}, false
	}
	layouts := []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02 3:04pm", "2006-01-02 3pm", "2006-01-02"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "tomorrow at ") {
		clock := strings.TrimSpace(strings.TrimPrefix(lower, "tomorrow at "))
		if parsedClock, ok := parseClock(clock); ok {
			year, month, day := now.UTC().Add(24 * time.Hour).Date()
			return time.Date(year, month, day, parsedClock.Hour(), parsedClock.Minute(), 0, 0, time.UTC), true
		}
	}
	return time.Time{}, false
}

func parseDateAndTime(dateText, timeText string, now time.Time) (time.Time, bool) {
	dateText = strings.TrimSpace(strings.ToLower(dateText))
	timeText = strings.TrimSpace(strings.ToLower(timeText))
	if dateText == "" || timeText == "" {
		return time.Time{}, false
	}
	var base time.Time
	switch dateText {
	case "tomorrow":
		year, month, day := now.UTC().Add(24 * time.Hour).Date()
		base = time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	default:
		parsedDate, err := time.Parse("2006-01-02", dateText)
		if err != nil {
			return time.Time{}, false
		}
		base = parsedDate.UTC()
	}
	clock, ok := parseClock(timeText)
	if !ok {
		return time.Time{}, false
	}
	return time.Date(base.Year(), base.Month(), base.Day(), clock.Hour(), clock.Minute(), 0, 0, time.UTC), true
}

func parseClock(text string) (time.Time, bool) {
	text = strings.TrimSpace(strings.ToLower(text))
	layouts := []string{"3:04pm", "3pm", "15:04"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	if strings.HasSuffix(text, "am") || strings.HasSuffix(text, "pm") {
		hourText := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(text, "am"), "pm"))
		if hour, err := strconv.Atoi(hourText); err == nil {
			suffix := "am"
			if strings.HasSuffix(text, "pm") {
				suffix = "pm"
			}
			if parsed, err := time.Parse("3pm", fmt.Sprintf("%d%s", hour, suffix)); err == nil {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
