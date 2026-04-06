package semantics

import "testing"

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

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
