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

func TestParseBundleNormalizesJourneyRootAndEdges(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
journeys:
  - id: flow_1
    when: [customer asks for help]
    states:
      - id: ask_name
        type: message
        instruction: What is your name?
        next: [ask_email]
      - id: ask_email
        type: message
        instruction: What is your email?
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
	if len(bundle.Journeys) != 1 {
		t.Fatalf("journeys len = %d, want 1", len(bundle.Journeys))
	}
	j := bundle.Journeys[0]
	if j.RootID != "ask_name" {
		t.Fatalf("journey root_id = %q, want ask_name", j.RootID)
	}
	if len(j.Edges) == 0 {
		t.Fatalf("journey edges = %#v, want compiled edges", j.Edges)
	}
	found := false
	for _, edge := range j.Edges {
		if edge.Source == "ask_name" && edge.Target == "ask_email" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("journey edges = %#v, want ask_name -> ask_email edge", j.Edges)
	}
}
