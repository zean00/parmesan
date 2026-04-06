package semantics

import "strings"

type phraseFamilyDef struct {
	Kind    Signal
	Phrases []string
}

type keywordFamilyDef struct {
	Signal Signal
	Parent Signal
	Tokens []string
}

type categoryFamilyDef struct {
	Category Category
	Terms    []Signal
}

type SlotExtractorDef struct {
	Kind       SlotKind
	Markers    []string
	StopTokens []string
}

type SignalRegistry struct {
	phraseFamilies  []phraseFamilyDef
	keywordFamilies []keywordFamilyDef
	stopwords       map[string]struct{}
	aliases         map[string]string
	relativeDates   []string
}

type CategoryRegistry struct {
	families []categoryFamilyDef
}

type SlotRegistry struct {
	fieldKinds map[string]SlotKind
	extractors map[SlotKind]SlotExtractorDef
}

var DefaultSignalRegistry = SignalRegistry{
	phraseFamilies: []phraseFamilyDef{
		{Kind: SignalReservation, Phrases: []string{"reserve a table", "book a table", "reservation"}},
		{Kind: SignalReturnStatus, Phrases: []string{"return status", "tracking"}},
		{Kind: SignalOrderStatus, Phrases: []string{"order status", "status is"}},
		{Kind: SignalPickup, Phrases: []string{"store pickup", "pick up", "pickup"}},
		{Kind: SignalInsideOutside, Phrases: []string{"inside", "outside"}},
		{Kind: SignalDrinkPreference, Phrases: []string{"drink", "drinks", "without drinks", "no drinks"}},
		{Kind: SignalApology, Phrases: []string{"sorry"}},
		{Kind: SignalCardLocked, Phrases: []string{"card is now locked", "locked your card", "lock_card", "locked"}},
	},
	keywordFamilies: []keywordFamilyDef{
		{Signal: SignalTracking, Parent: SignalUnknown, Tokens: []string{"tracking"}},
		{Signal: SignalUnknown, Parent: SignalUnknown, Tokens: []string{"refund", "return", "damaged", "cancel", "order"}},
		{Signal: SignalScheduling, Parent: SignalUnknown, Tokens: []string{"schedule", "appointment", "booking", "book", "reschedule"}},
		{Signal: SignalConfirmation, Parent: SignalUnknown, Tokens: []string{"confirm", "confirmation", "notify", "email"}},
		{Signal: SignalVehicle, Parent: SignalUnknown, Tokens: []string{"vehicle", "motorcycle", "car", "bike"}},
		{Signal: SignalTemperature, Parent: SignalUnknown, Tokens: []string{"temperature", "indoor", "outdoor", "weather"}},
		{Signal: SignalSearch, Parent: SignalUnknown, Tokens: []string{"product", "search", "catalog", "inventory"}},
	},
	stopwords: map[string]struct{}{
		"the": {}, "a": {}, "an": {}, "and": {}, "or": {}, "to": {}, "for": {}, "with": {}, "of": {}, "is": {}, "are": {}, "be": {}, "i": {}, "you": {}, "my": {}, "your": {}, "it": {}, "this": {}, "that": {}, "do": {}, "does": {},
	},
	aliases: map[string]string{
		"hi":        "hello",
		"hey":       "hello",
		"greetings": "hello",
		"greet":     "hello",
		"greeting":  "hello",
		"says":      "say",
		"said":      "say",
		"saying":    "say",
	},
	relativeDates: []string{"today", "tomorrow", "next week", "next month", "return in"},
}

var DefaultCategoryRegistry = CategoryRegistry{
	families: []categoryFamilyDef{
		{Category: CategoryVehicle, Terms: []Signal{SignalVehicle, "motorcycle", "car", "bike"}},
		{Category: CategoryTemperature, Terms: []Signal{SignalTemperature, "indoor", "outdoor", "weather"}},
		{Category: CategorySearch, Terms: []Signal{SignalSearch, "product", "catalog", "inventory"}},
		{Category: CategoryScheduling, Terms: []Signal{SignalScheduling, "schedule", "appointment", "booking", "book", "reschedule"}},
		{Category: CategoryConfirmation, Terms: []Signal{SignalConfirmation, "confirm", "notify", "email"}},
	},
}

var DefaultSlotRegistry = SlotRegistry{
	fieldKinds: map[string]SlotKind{
		"destination":  SlotDestination,
		"model":        SlotProductLike,
		"product_name": SlotProductLike,
		"query":        SlotProductLike,
	},
	extractors: map[SlotKind]SlotExtractorDef{
		SlotDestination: {Kind: SlotDestination, Markers: []string{"to"}, StopTokens: []string{"today", "tomorrow", "next", "return", "for"}},
		SlotProductLike: {Kind: SlotProductLike, Markers: []string{"for a", "for an", "for the", "for"}, StopTokens: []string{"today", "tomorrow", "next", "with", "from", "to"}},
	},
}

func NormalizedTokens(input string) []string {
	input = strings.NewReplacer("_", " ", "/", " ", "-", " ").Replace(input)
	raw := strings.Fields(strings.ToLower(input))
	var out []string
	for _, token := range raw {
		token = strings.Trim(token, ".,!?;:\"'()[]{}")
		if canonical, ok := DefaultSignalRegistry.aliases[token]; ok {
			token = canonical
		}
		if len(token) < 3 {
			continue
		}
		if _, ok := DefaultSignalRegistry.stopwords[token]; ok {
			continue
		}
		out = append(out, token)
	}
	return out
}

func Signals(input string) []string {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return nil
	}
	var out []string
	for _, token := range NormalizedTokens(input) {
		out = append(out, token)
		for _, family := range DefaultSignalRegistry.keywordFamilies {
			for _, candidate := range family.Tokens {
				if token != candidate {
					continue
				}
				if family.Signal != SignalUnknown {
					out = append(out, string(family.Signal))
				}
				if family.Parent != SignalUnknown {
					out = append(out, string(family.Parent))
				}
			}
		}
	}
	base := SignalSet(out)
	if DefaultSignalRegistry.HasPhraseFamily(input, SignalReservation) {
		out = append(out, string(SignalReservation))
	}
	if kind := statusSignal(input, base); kind != SignalUnknown {
		out = append(out, string(kind))
	}
	if kind := deliverySignal(input); kind != SignalUnknown {
		out = append(out, string(kind))
	}
	if kind := choiceSignal(input); kind != SignalUnknown {
		out = append(out, string(kind))
	}
	return dedupeStrings(out)
}

func CanonicalKeywordFamily(token string) (string, bool) {
	for _, family := range DefaultSignalRegistry.keywordFamilies {
		for _, candidate := range family.Tokens {
			if token != candidate {
				continue
			}
			if family.Signal == SignalUnknown && family.Parent == SignalUnknown {
				return "", true
			}
			if family.Parent != SignalUnknown {
				return string(family.Parent), true
			}
			return string(family.Signal), true
		}
	}
	return "", false
}

func SignalSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out[item] = struct{}{}
	}
	return out
}

func Categories(terms []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, term := range terms {
		for _, family := range DefaultCategoryRegistry.families {
			for _, candidate := range family.Terms {
				if term == string(candidate) {
					out[string(family.Category)] = struct{}{}
					break
				}
			}
		}
	}
	return out
}

func SlotKindForField(field string) SlotKind {
	if kind, ok := DefaultSlotRegistry.fieldKinds[strings.ToLower(strings.TrimSpace(field))]; ok {
		return kind
	}
	return SlotUnknown
}

func RelativeDateTerm(text string) string {
	for _, marker := range DefaultSignalRegistry.relativeDates {
		if strings.Contains(strings.ToLower(text), marker) {
			return marker
		}
	}
	return ""
}

func (r SignalRegistry) HasPhraseFamily(text string, kind Signal) bool {
	for _, family := range r.phraseFamilies {
		if family.Kind != kind {
			continue
		}
		for _, phrase := range family.Phrases {
			if strings.Contains(text, phrase) {
				return true
			}
		}
		return false
	}
	return false
}

func hasAnySignal(set map[string]struct{}, signals ...Signal) bool {
	for _, signal := range signals {
		if _, ok := set[string(signal)]; ok {
			return true
		}
	}
	return false
}

func statusSignal(text string, signals map[string]struct{}) Signal {
	switch {
	case hasAnySignal(signals, SignalReturnStatus, SignalTracking) || DefaultSignalRegistry.HasPhraseFamily(text, SignalReturnStatus):
		return SignalReturnStatus
	case hasAnySignal(signals, SignalOrderStatus) || DefaultSignalRegistry.HasPhraseFamily(text, SignalOrderStatus):
		return SignalOrderStatus
	default:
		return SignalUnknown
	}
}

func deliverySignal(text string) Signal {
	switch {
	case DefaultSignalRegistry.HasPhraseFamily(text, SignalPickup):
		return SignalPickup
	case strings.Contains(text, "delivery"):
		return SignalDelivery
	default:
		return SignalUnknown
	}
}

func choiceSignal(text string) Signal {
	switch {
	case DefaultSignalRegistry.HasPhraseFamily(text, SignalInsideOutside):
		return SignalInsideOutside
	case DefaultSignalRegistry.HasPhraseFamily(text, SignalDrinkPreference):
		return SignalDrinkPreference
	default:
		return SignalUnknown
	}
}

func SlotExtractorForKind(kind SlotKind) (SlotExtractorDef, bool) {
	extractor, ok := DefaultSlotRegistry.extractors[kind]
	return extractor, ok
}

func dedupeStrings(items []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
