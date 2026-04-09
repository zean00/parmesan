package artifactmeta

import "time"

type Scope struct {
	Org     string `json:"org,omitempty"`
	Team    string `json:"team,omitempty"`
	Brand   string `json:"brand,omitempty"`
	Channel string `json:"channel,omitempty"`
	Product string `json:"product,omitempty"`
	Region  string `json:"region,omitempty"`
	Locale  string `json:"locale,omitempty"`
	Segment string `json:"segment,omitempty"`
}

type Meta struct {
	OrgID         string    `json:"org_id,omitempty"`
	Kind          string    `json:"kind,omitempty"`
	Source        string    `json:"source,omitempty"`
	Scope         Scope     `json:"scope,omitempty"`
	RiskTier      string    `json:"risk_tier,omitempty"`
	LineageRootID string    `json:"lineage_root_id,omitempty"`
	Version       string    `json:"version,omitempty"`
	EvidenceRefs  []string  `json:"evidence_refs,omitempty"`
	CreatedBy     string    `json:"created_by,omitempty"`
	ApprovedBy    string    `json:"approved_by,omitempty"`
	EffectiveFrom time.Time `json:"effective_from,omitempty"`
	EffectiveTo   time.Time `json:"effective_to,omitempty"`
	ProposalID    string    `json:"proposal_id,omitempty"`
	TraceID       string    `json:"trace_id,omitempty"`
}

func (m Meta) IsZero() bool {
	return m.OrgID == "" &&
		m.Kind == "" &&
		m.Source == "" &&
		m.RiskTier == "" &&
		m.LineageRootID == "" &&
		m.Version == "" &&
		len(m.EvidenceRefs) == 0 &&
		m.CreatedBy == "" &&
		m.ApprovedBy == "" &&
		m.ProposalID == "" &&
		m.TraceID == "" &&
		m.EffectiveFrom.IsZero() &&
		m.EffectiveTo.IsZero() &&
		m.Scope == (Scope{})
}
