package semantics

import (
	"testing"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func TestSignalsAliasAndPhraseFamilies(t *testing.T) {
	t.Parallel()

	got := Signals("Hey, can you help me track my return status?")
	if !containsString(got, "hello") {
		t.Fatalf("Signals() = %#v, want alias-normalized hello", got)
	}
	if !containsString(got, string(SignalReturnStatus)) {
		t.Fatalf("Signals() = %#v, want return_status", got)
	}
}

func TestRelativeDateTerm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		text string
		want string
	}{
		{text: "please book it for tomorrow", want: "tomorrow"},
		{text: "I will return in two weeks", want: "return in"},
		{text: "no date here", want: ""},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.text, func(t *testing.T) {
			t.Parallel()
			if got := RelativeDateTerm(tc.text); got != tc.want {
				t.Fatalf("RelativeDateTerm(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestSlotKindForField(t *testing.T) {
	t.Parallel()

	if got := SlotKindForField("destination"); got != SlotDestination {
		t.Fatalf("SlotKindForField(destination) = %q, want %q", got, SlotDestination)
	}
	if got := SlotKindForField("query"); got != SlotProductLike {
		t.Fatalf("SlotKindForField(query) = %q, want %q", got, SlotProductLike)
	}
	if got := SlotKindForField("unknown_field"); got != SlotUnknown {
		t.Fatalf("SlotKindForField(unknown_field) = %q, want %q", got, SlotUnknown)
	}
}

func TestCategories(t *testing.T) {
	t.Parallel()

	got := Categories([]string{"motorcycle", "schedule", "confirm"})
	if _, ok := got[string(CategoryVehicle)]; !ok {
		t.Fatalf("Categories() = %#v, want vehicle", got)
	}
	if _, ok := got[string(CategoryScheduling)]; !ok {
		t.Fatalf("Categories() = %#v, want scheduling", got)
	}
	if _, ok := got[string(CategoryConfirmation)]; !ok {
		t.Fatalf("Categories() = %#v, want confirmation", got)
	}
}

func TestSignalsForPolicyUsesAuthoredSemanticPack(t *testing.T) {
	t.Parallel()

	sem := policy.SemanticsPolicy{
		Signals: []policy.SemanticSignal{
			{ID: "parcel_update", Tokens: []string{"parcel", "courier"}},
		},
		RelativeDates: []string{"next friday"},
		Categories: []policy.SemanticCategory{
			{ID: "logistics", Signals: []string{"parcel_update"}},
		},
		Slots: []policy.SemanticSlot{
			{Field: "tracking_code", Kind: "product_like"},
		},
	}
	got := SignalsForPolicy(sem, "Please ask the courier for my parcel update next Friday")
	if !containsString(got, "parcel_update") {
		t.Fatalf("SignalsForPolicy() = %#v, want parcel_update", got)
	}
	if got := RelativeDateTermPolicy(sem, "check again next friday"); got != "next friday" {
		t.Fatalf("RelativeDateTermPolicy() = %q, want next friday", got)
	}
	if got := SlotKindForFieldPolicy(sem, "tracking_code"); got != SlotProductLike {
		t.Fatalf("SlotKindForFieldPolicy() = %q, want product_like", got)
	}
	if cats := CategoriesForPolicy(sem, []string{"parcel_update"}); len(cats) != 1 {
		t.Fatalf("CategoriesForPolicy() = %#v, want logistics category", cats)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
