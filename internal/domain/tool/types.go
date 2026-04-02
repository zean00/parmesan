package tool

import "time"

type ProviderKind string

const (
	ProviderNative  ProviderKind = "native"
	ProviderMCP     ProviderKind = "mcp_remote"
	ProviderOpenAPI ProviderKind = "openapi_remote"
)

type ProviderBinding struct {
	ID           string       `json:"id"`
	Kind         ProviderKind `json:"kind"`
	Name         string       `json:"name"`
	URI          string       `json:"uri"`
	RegisteredAt time.Time    `json:"registered_at"`
	Healthy      bool         `json:"healthy"`
}

type AuthType string

const (
	AuthBearer AuthType = "bearer"
	AuthHeader AuthType = "header"
)

type AuthBinding struct {
	ProviderID string    `json:"provider_id"`
	Type       AuthType  `json:"type"`
	HeaderName string    `json:"header_name,omitempty"`
	Secret     string    `json:"secret,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type CatalogEntry struct {
	ID              string    `json:"id"`
	ProviderID      string    `json:"provider_id"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Schema          string    `json:"schema"`
	RuntimeProtocol string    `json:"runtime_protocol"`
	MetadataJSON    string    `json:"metadata_json,omitempty"`
	ImportedAt      time.Time `json:"imported_at"`
}
