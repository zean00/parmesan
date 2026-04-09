package semantics

import (
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
)

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
	phraseIndex     map[Signal][]string
}

type CategoryRegistry struct {
	families []categoryFamilyDef
}

type SlotRegistry struct {
	fieldKinds map[string]SlotKind
	extractors map[SlotKind]SlotExtractorDef
}

type keywordSignalMapping struct {
	Signal Signal
	Parent Signal
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
	phraseIndex: buildPhraseFamilyIndex([]phraseFamilyDef{
		{Kind: SignalReservation, Phrases: []string{"reserve a table", "book a table", "reservation"}},
		{Kind: SignalReturnStatus, Phrases: []string{"return status", "tracking"}},
		{Kind: SignalOrderStatus, Phrases: []string{"order status", "status is"}},
		{Kind: SignalPickup, Phrases: []string{"store pickup", "pick up", "pickup"}},
		{Kind: SignalInsideOutside, Phrases: []string{"inside", "outside"}},
		{Kind: SignalDrinkPreference, Phrases: []string{"drink", "drinks", "without drinks", "no drinks"}},
		{Kind: SignalApology, Phrases: []string{"sorry"}},
		{Kind: SignalCardLocked, Phrases: []string{"card is now locked", "locked your card", "lock_card", "locked"}},
	}),
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

var (
	normalizationReplacer = strings.NewReplacer("_", " ", "/", " ", "-", " ")
	keywordSignalIndex    = buildKeywordSignalIndex(DefaultSignalRegistry.keywordFamilies)
)

func signalRegistryFromPolicy(sem policy.SemanticsPolicy) SignalRegistry {
	if len(sem.Signals) == 0 && len(sem.RelativeDates) == 0 {
		return DefaultSignalRegistry
	}
	registry := SignalRegistry{
		stopwords:     DefaultSignalRegistry.stopwords,
		aliases:       DefaultSignalRegistry.aliases,
		relativeDates: append([]string(nil), DefaultSignalRegistry.relativeDates...),
	}
	if len(sem.RelativeDates) > 0 {
		registry.relativeDates = append([]string(nil), sem.RelativeDates...)
	}
	for _, item := range sem.Signals {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if len(item.Phrases) > 0 {
			registry.phraseFamilies = append(registry.phraseFamilies, phraseFamilyDef{Kind: Signal(id), Phrases: append([]string(nil), item.Phrases...)})
		}
		tokens := append([]string(nil), item.Tokens...)
		tokens = append(tokens, item.Aliases...)
		if len(tokens) > 0 {
			registry.keywordFamilies = append(registry.keywordFamilies, keywordFamilyDef{Signal: Signal(id), Parent: Signal(strings.TrimSpace(item.Parent)), Tokens: tokens})
		}
	}
	registry.phraseIndex = buildPhraseFamilyIndex(registry.phraseFamilies)
	return registry
}

func categoryRegistryFromPolicy(sem policy.SemanticsPolicy) CategoryRegistry {
	if len(sem.Categories) == 0 {
		return DefaultCategoryRegistry
	}
	registry := CategoryRegistry{}
	for _, item := range sem.Categories {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		family := categoryFamilyDef{Category: Category(id)}
		for _, signal := range item.Signals {
			signal = strings.TrimSpace(signal)
			if signal != "" {
				family.Terms = append(family.Terms, Signal(signal))
			}
		}
		registry.families = append(registry.families, family)
	}
	return registry
}

func slotRegistryFromPolicy(sem policy.SemanticsPolicy) SlotRegistry {
	if len(sem.Slots) == 0 {
		return DefaultSlotRegistry
	}
	registry := SlotRegistry{
		fieldKinds: map[string]SlotKind{},
		extractors: map[SlotKind]SlotExtractorDef{},
	}
	for _, item := range sem.Slots {
		field := strings.ToLower(strings.TrimSpace(item.Field))
		kind := SlotKind(strings.TrimSpace(item.Kind))
		if field == "" || kind == SlotUnknown {
			continue
		}
		registry.fieldKinds[field] = kind
		registry.extractors[kind] = SlotExtractorDef{
			Kind:       kind,
			Markers:    append([]string(nil), item.Markers...),
			StopTokens: append([]string(nil), item.StopTokens...),
		}
	}
	return registry
}

func buildPhraseFamilyIndex(families []phraseFamilyDef) map[Signal][]string {
	out := make(map[Signal][]string, len(families))
	for _, family := range families {
		out[family.Kind] = append(out[family.Kind], family.Phrases...)
	}
	return out
}

func buildKeywordSignalIndex(families []keywordFamilyDef) map[string][]keywordSignalMapping {
	out := map[string][]keywordSignalMapping{}
	for _, family := range families {
		for _, token := range family.Tokens {
			out[token] = append(out[token], keywordSignalMapping{
				Signal: family.Signal,
				Parent: family.Parent,
			})
		}
	}
	return out
}

func normalizedTokensLowered(input string) []string {
	raw := strings.Fields(normalizationReplacer.Replace(input))
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

func NormalizedTokens(input string) []string {
	return normalizedTokensLowered(strings.ToLower(input))
}

func SignalsForPolicy(sem policy.SemanticsPolicy, input string) []string {
	return signalsWithRegistry(signalRegistryFromPolicy(sem), input)
}

func Signals(input string) []string {
	return signalsWithRegistry(DefaultSignalRegistry, input)
}

func signalsWithRegistry(registry SignalRegistry, input string) []string {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return nil
	}
	index := buildKeywordSignalIndex(registry.keywordFamilies)
	var out []string
	for _, token := range normalizedTokensLowered(input) {
		out = append(out, token)
		for _, mapping := range index[token] {
			if mapping.Signal != SignalUnknown {
				out = append(out, string(mapping.Signal))
			}
			if mapping.Parent != SignalUnknown {
				out = append(out, string(mapping.Parent))
			}
		}
	}
	base := SignalSet(out)
	if registry.HasPhraseFamily(input, SignalReservation) {
		out = append(out, string(SignalReservation))
	}
	if kind := statusSignalWithRegistry(registry, input, base); kind != SignalUnknown {
		out = append(out, string(kind))
	}
	if kind := deliverySignalWithRegistry(registry, input); kind != SignalUnknown {
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
	return CategoriesForPolicy(policy.SemanticsPolicy{}, terms)
}

func CategoriesForPolicy(sem policy.SemanticsPolicy, terms []string) map[string]struct{} {
	registry := categoryRegistryFromPolicy(sem)
	out := map[string]struct{}{}
	for _, term := range terms {
		for _, family := range registry.families {
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
	return SlotKindForFieldPolicy(policy.SemanticsPolicy{}, field)
}

func SlotKindForFieldPolicy(sem policy.SemanticsPolicy, field string) SlotKind {
	registry := slotRegistryFromPolicy(sem)
	if kind, ok := registry.fieldKinds[strings.ToLower(strings.TrimSpace(field))]; ok {
		return kind
	}
	return SlotUnknown
}

func RelativeDateTerm(text string) string {
	return RelativeDateTermPolicy(policy.SemanticsPolicy{}, text)
}

func RelativeDateTermPolicy(sem policy.SemanticsPolicy, text string) string {
	registry := signalRegistryFromPolicy(sem)
	for _, marker := range registry.relativeDates {
		if strings.Contains(strings.ToLower(text), marker) {
			return marker
		}
	}
	return ""
}

func (r SignalRegistry) HasPhraseFamily(text string, kind Signal) bool {
	if families := r.phraseIndex[kind]; len(families) > 0 {
		for _, phrase := range families {
			if strings.Contains(text, phrase) {
				return true
			}
		}
		return false
	}
	for _, family := range r.phraseFamilies {
		if family.Kind != kind {
			continue
		}
		for _, phrase := range family.Phrases {
			if strings.Contains(text, phrase) {
				return true
			}
		}
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
	return statusSignalWithRegistry(DefaultSignalRegistry, text, signals)
}

func statusSignalWithRegistry(registry SignalRegistry, text string, signals map[string]struct{}) Signal {
	switch {
	case hasAnySignal(signals, SignalReturnStatus, SignalTracking) || registry.HasPhraseFamily(text, SignalReturnStatus):
		return SignalReturnStatus
	case hasAnySignal(signals, SignalOrderStatus) || registry.HasPhraseFamily(text, SignalOrderStatus):
		return SignalOrderStatus
	default:
		return SignalUnknown
	}
}

func deliverySignal(text string) Signal {
	return deliverySignalWithRegistry(DefaultSignalRegistry, text)
}

func deliverySignalWithRegistry(registry SignalRegistry, text string) Signal {
	switch {
	case registry.HasPhraseFamily(text, SignalPickup):
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
