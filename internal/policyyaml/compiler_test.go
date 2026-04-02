package policyyaml

import "testing"

func TestParseBundle(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
guidelines:
  - id: greet
    when: customer says hello
    then: greet them back
    mcp:
      server: crm
      tool: create_contact
journeys:
  - id: flow_1
    when: [customer asks for help]
    states:
      - id: lookup
        type: tool
        mcp:
          server: commerce
          tool: get_order
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}

	if bundle.ID != "bundle-1" {
		t.Fatalf("bundle ID = %q, want bundle-1", bundle.ID)
	}

	if len(bundle.Guidelines) != 1 {
		t.Fatalf("guidelines len = %d, want 1", len(bundle.Guidelines))
	}
	if len(bundle.GuidelineToolAssociations) != 2 {
		t.Fatalf("guideline tool associations = %#v, want 2 compiled associations", bundle.GuidelineToolAssociations)
	}
}

func TestValidateBundleRejectsDuplicateIDs(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
guidelines:
  - id: dup
    when: one
    then: one
templates:
  - id: dup
    mode: strict
    text: hi
`)

	if _, err := ParseBundle(raw); err == nil {
		t.Fatal("ParseBundle() error = nil, want duplicate id error")
	}
}
