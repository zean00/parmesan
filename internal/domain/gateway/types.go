package gateway

import "time"

type CapabilityProfile struct {
	SupportsText               bool  `json:"supports_text"`
	SupportsImageUpload        bool  `json:"supports_image_upload"`
	SupportsInteractiveApproval bool `json:"supports_interactive_approval"`
	SupportsThreads            bool  `json:"supports_threads"`
	MaxAttachmentBytes         int64 `json:"max_attachment_bytes,omitempty"`
}

type ConversationBinding struct {
	ID                     string            `json:"id"`
	Channel                string            `json:"channel"`
	ExternalConversationID string            `json:"external_conversation_id"`
	ExternalUserID         string            `json:"external_user_id,omitempty"`
	SessionID              string            `json:"session_id"`
	CapabilityProfile      CapabilityProfile `json:"capability_profile"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}
