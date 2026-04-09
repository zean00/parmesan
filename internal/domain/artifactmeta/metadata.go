package artifactmeta

const MetadataKey = "artifact_meta"

func Merge(metadata map[string]any, meta Meta) map[string]any {
	out := cloneMap(metadata)
	if meta.IsZero() {
		delete(out, MetadataKey)
		return out
	}
	out[MetadataKey] = meta
	return out
}

func Extract(metadata map[string]any) (Meta, map[string]any) {
	out := cloneMap(metadata)
	raw, ok := out[MetadataKey]
	if !ok {
		return Meta{}, out
	}
	delete(out, MetadataKey)
	meta := normalize(raw)
	return meta, out
}

func normalize(v any) Meta {
	switch item := v.(type) {
	case Meta:
		return item
	case *Meta:
		if item == nil {
			return Meta{}
		}
		return *item
	case map[string]any:
		return Meta{
			OrgID:  stringValue(item["org_id"]),
			Kind:   stringValue(item["kind"]),
			Source: stringValue(item["source"]),
			Scope: Scope{
				Org:     stringValue(nestedMap(item["scope"])["org"]),
				Team:    stringValue(nestedMap(item["scope"])["team"]),
				Brand:   stringValue(nestedMap(item["scope"])["brand"]),
				Channel: stringValue(nestedMap(item["scope"])["channel"]),
				Product: stringValue(nestedMap(item["scope"])["product"]),
				Region:  stringValue(nestedMap(item["scope"])["region"]),
				Locale:  stringValue(nestedMap(item["scope"])["locale"]),
				Segment: stringValue(nestedMap(item["scope"])["segment"]),
			},
			RiskTier:      stringValue(item["risk_tier"]),
			LineageRootID: stringValue(item["lineage_root_id"]),
			Version:       stringValue(item["version"]),
			EvidenceRefs:  stringSlice(item["evidence_refs"]),
			CreatedBy:     stringValue(item["created_by"]),
			ApprovedBy:    stringValue(item["approved_by"]),
			ProposalID:    stringValue(item["proposal_id"]),
			TraceID:       stringValue(item["trace_id"]),
		}
	default:
		return Meta{}
	}
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func nestedMap(v any) map[string]any {
	item, ok := v.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return item
}

func stringValue(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func stringSlice(v any) []string {
	switch item := v.(type) {
	case []string:
		return append([]string(nil), item...)
	case []any:
		out := make([]string, 0, len(item))
		for _, v := range item {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
