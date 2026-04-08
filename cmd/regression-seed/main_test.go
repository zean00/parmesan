package main

import "testing"

func TestInferCategory(t *testing.T) {
	if got := inferCategory([]string{"topic_scope_compliance"}, nil); got != "topic_scope" {
		t.Fatalf("inferCategory(topic_scope) = %q", got)
	}
	if got := inferCategory([]string{"customer_preference"}, nil); got != "preference" {
		t.Fatalf("inferCategory(customer_preference) = %q", got)
	}
	if got := inferCategory([]string{"knowledge_grounding"}, nil); got != "knowledge_grounding" {
		t.Fatalf("inferCategory(knowledge_grounding) = %q", got)
	}
}

func TestInferDomain(t *testing.T) {
	if got := inferDomain("operator_feedback_answered_out_of_scope_pet"); got != "pet_store" {
		t.Fatalf("inferDomain(pet) = %q", got)
	}
	if got := inferDomain("operator_feedback_unsupported_claim_refund"); got != "ecommerce" {
		t.Fatalf("inferDomain(ecommerce) = %q", got)
	}
	if got := inferDomain("operator_feedback_bad_language"); got != "support" {
		t.Fatalf("inferDomain(default) = %q", got)
	}
}
