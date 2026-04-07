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

func TestParseBundleWithSoul(t *testing.T) {
	bundle, err := ParseBundle([]byte(`
id: bundle
version: v1
soul:
  identity: Parmesan Support
  role: customer support agent
  brand: Parmesan
  default_language: en
  supported_languages: [en, id]
  language_matching: reply in the customer's language unless policy requires otherwise
  tone: calm and practical
  formality: semi-formal
  verbosity: concise
  style_rules:
    - ask one question at a time
  avoid_rules:
    - unsupported promises
  escalation_style: acknowledge the reason and explain the next step
  formatting_rules:
    - use short paragraphs
guidelines:
  - id: returns
    when: customer asks about returns
    then: explain the return process
`))
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
	if bundle.Soul.Brand != "Parmesan" || bundle.Soul.DefaultLanguage != "en" || len(bundle.Soul.SupportedLanguages) != 2 {
		t.Fatalf("soul = %#v, want parsed language and brand fields", bundle.Soul)
	}
	if len(bundle.Soul.StyleRules) != 1 || bundle.Soul.StyleRules[0] != "ask one question at a time" {
		t.Fatalf("soul style rules = %#v, want parsed style rule", bundle.Soul.StyleRules)
	}
}

func TestParseBundleRejectsInvalidSoul(t *testing.T) {
	_, err := ParseBundle([]byte(`
id: bundle
version: v1
soul:
  default_language: en_US
  supported_languages: [en, en]
  style_rules:
    - keep replies concise
    - keep replies concise
`))
	if err == nil {
		t.Fatal("ParseBundle() error = nil, want invalid soul error")
	}
}
