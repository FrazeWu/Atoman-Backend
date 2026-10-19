package user_theme

import "encoding/json"

// UserThemeDTO represents the theme data returned in API responses.
type UserThemeDTO struct {
	IsEnabled    bool            `json:"is_enabled"`
	Mode         string          `json:"mode"`
	PresetConfig json.RawMessage `json:"preset_config" swaggertype:"object"`
	CustomCSS    string          `json:"custom_css"`
	SanitizedCSS string          `json:"sanitized_css"`
}

// UpdateThemeInput represents the request payload for updating a user's theme.
type UpdateThemeInput struct {
	IsEnabled    *bool           `json:"is_enabled"`
	Mode         string          `json:"mode"`
	PresetConfig json.RawMessage `json:"preset_config" swaggertype:"object"`
	CustomCSS    string          `json:"custom_css"`
}
