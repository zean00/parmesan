package orbyte

import "testing"

func TestExtractCustomerTargetStopsAtFollowUpPhrases(t *testing.T) {
	text := "I'm also evaluating Espresso Double 20260417-000008 for CRM Demo Customer 20260417000007. Please tell me the key product details and why it fits a compact counter setup, then have sales follow up if it looks suitable."
	got := extractCustomerTarget(text)
	want := "CRM Demo Customer 20260417000007"
	if got != want {
		t.Fatalf("extractCustomerTarget() = %q, want %q", got, want)
	}
}

func TestExtractProductTargetDoesNotSplitInsideEspresso(t *testing.T) {
	text := "I'm also evaluating Espresso Double 20260417-012055 for CRM Demo Customer 20260417012053, so please tell me the key product details."
	got := extractProductTarget(text)
	want := "Espresso Double 20260417-012055"
	if got != want {
		t.Fatalf("extractProductTarget() = %q, want %q", got, want)
	}
}

func TestResolveProductInterestLeadArgsSetsConfirmApply(t *testing.T) {
	got := resolveProductInterestLeadArgs(
		"I'm also evaluating Espresso Double 20260417-012055 for CRM Demo Customer 20260417012053.",
		map[string]struct{}{
			"product_name":  {},
			"party_name":    {},
			"confirm_apply": {},
		},
	)
	if got["confirm_apply"] != true {
		t.Fatalf("confirm_apply = %#v, want true", got["confirm_apply"])
	}
	if got["product_name"] != "Espresso Double 20260417-012055" {
		t.Fatalf("product_name = %#v", got["product_name"])
	}
	if got["party_name"] != "CRM Demo Customer 20260417012053" {
		t.Fatalf("party_name = %#v", got["party_name"])
	}
}
