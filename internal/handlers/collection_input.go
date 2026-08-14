package handlers

import (
	"bytes"
	"encoding/json"

	"github.com/google/uuid"
)

// nullableUUIDInput distinguishes an omitted field from an explicit JSON null.
type nullableUUIDInput struct {
	Set   bool
	Value *uuid.UUID
}

func (input *nullableUUIDInput) UnmarshalJSON(data []byte) error {
	input.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		input.Value = nil
		return nil
	}
	var value uuid.UUID
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	input.Value = &value
	return nil
}
