package vendor

import "time"

// Profile holds the configuration for a single external vendor API endpoint.
type Profile struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	TargetURL    string            `json:"target_url"`
	Method       string            `json:"method"`
	AuthHeaders  map[string]string `json:"auth_headers"`
	BodyTemplate string            `json:"body_template"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// CreateRequest is the request body for creating a vendor profile.
type CreateRequest struct {
	Name         string            `json:"name"`
	TargetURL    string            `json:"target_url"`
	Method       string            `json:"method"`
	AuthHeaders  map[string]string `json:"auth_headers"`
	BodyTemplate string            `json:"body_template"`
}

// UpdateRequest is the request body for updating a vendor profile.
type UpdateRequest struct {
	Name         *string            `json:"name"`
	TargetURL    *string            `json:"target_url"`
	Method       *string            `json:"method"`
	AuthHeaders  map[string]string  `json:"auth_headers"`
	BodyTemplate *string            `json:"body_template"`
}
