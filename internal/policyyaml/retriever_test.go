package policyyaml

import "testing"

func TestParseBundleWithRetrieverBinding(t *testing.T) {
	_, err := ParseBundle([]byte(`
id: bundle
version: v1
guidelines:
  - id: damaged_return
    when: damaged order
    then: explain returns
retrievers:
  - id: wiki
    kind: knowledge
    scope: guideline
    target_id: damaged_return
    max_results: 2
`))
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
}

func TestParseBundleRejectsInvalidRetrieverBinding(t *testing.T) {
	_, err := ParseBundle([]byte(`
id: bundle
version: v1
retrievers:
  - id: wiki
    kind: knowledge
    scope: guideline
`))
	if err == nil {
		t.Fatal("ParseBundle() error = nil, want missing target_id error")
	}
}
