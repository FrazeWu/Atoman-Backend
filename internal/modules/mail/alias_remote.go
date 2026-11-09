package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The administrator credential is used only on the server. Never accept a
// Stalwart account ID, domain ID or alias slot from the browser.
type MailAliasRemote interface {
	AddAlias(context.Context, StalwartCredential, string, string, string) (uint32, error)
	SetAliasEnabled(context.Context, StalwartCredential, string, string, string, uint32, bool) error
}

type stalwartAccountAlias struct {
	Name     string `json:"name"`
	DomainID string `json:"domainId"`
	Enable   bool   `json:"enable"`
}

// Stalwart 1.0.2 returns aliases as a numeric-keyed JSON object (not an
// array). The slot IDs are part of the remote mutation identity; never sort
// aliases by name or compact a sparse map, which could modify another alias.
func decodeStalwartAccountAliases(raw json.RawMessage) ([]stalwartAccountAlias, error) {
	if len(raw) == 0 {
		return nil, errors.New("Remote aliases property missing")
	}
	if string(raw) == "null" {
		return []stalwartAccountAlias{}, nil
	}
	var entries []stalwartAccountAlias
	switch raw[0] {
	case '[':
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, errors.New("Invalid remote alias array")
		}
	case '{':
		var slots map[string]stalwartAccountAlias
		if err := json.Unmarshal(raw, &slots); err != nil {
			return nil, errors.New("Invalid remote alias object")
		}
		if len(slots) > 25 {
			return nil, errors.New("Remote alias slot count exceeded")
		}
		entries = make([]stalwartAccountAlias, len(slots))
		seen := make([]bool, len(slots))
		for key, alias := range slots {
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(entries) || strconv.Itoa(i) != key || seen[i] {
				return nil, errors.New("Remote alias slot mapping is unsafe")
			}
			entries[i] = alias
			seen[i] = true
		}
		for _, ok := range seen {
			if !ok {
				return nil, errors.New("Remote alias slots are sparse")
			}
		}
	default:
		return nil, errors.New("Unknown remote alias JSON representation")
	}
	if len(entries) > 25 {
		return nil, errors.New("Remote alias slot count exceeded")
	}
	for _, alias := range entries {
		if alias.Name == "" || !validJMAPScopedID(alias.DomainID) {
			return nil, errors.New("Invalid remote alias identity")
		}
	}
	return entries, nil
}

func (s *StalwartJMAPClient) readAccountAliases(ctx context.Context, admin StalwartCredential, accountID string) ([]stalwartAccountAlias, error) {
	if !validJMAPScopedID(accountID) {
		return nil, errors.New("Invalid Stalwart alias target")
	}
	var resp struct {
		List []struct {
			ID      string          `json:"id"`
			Aliases json.RawMessage `json:"aliases"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	if err := s.call(ctx, admin, true, "x:Account/get", map[string]any{"ids": []string{accountID}, "properties": []string{"id", "aliases"}}, &resp); err != nil {
		return nil, err
	}
	if len(resp.NotFound) > 0 || len(resp.List) != 1 || resp.List[0].ID != accountID {
		return nil, errors.New("Stalwart alias target mismatch")
	}
	return decodeStalwartAccountAliases(resp.List[0].Aliases)
}
func (s *StalwartJMAPClient) patchAlias(ctx context.Context, admin StalwartCredential, accountID, key string, value any) error {
	var resp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := s.call(ctx, admin, true, "x:Account/set", map[string]any{"update": map[string]any{accountID: map[string]any{key: value}}}, &resp); err != nil {
		return err
	}
	if len(resp.NotUpdated) != 0 {
		return errors.New("Stalwart alias update rejected")
	}
	if _, ok := resp.Updated[accountID]; !ok || len(resp.Updated) != 1 {
		return errors.New("Stalwart alias update not confirmed")
	}
	return nil
}
func (s *StalwartJMAPClient) AddAlias(ctx context.Context, admin StalwartCredential, accountID, domainID, localPart string) (uint32, error) {
	if !validJMAPScopedID(domainID) || localPart == "" {
		return 0, errors.New("Invalid alias domain")
	}
	aliases, err := s.readAccountAliases(ctx, admin, accountID)
	if err != nil {
		return 0, err
	}
	for _, item := range aliases {
		if strings.EqualFold(item.Name, localPart) && item.DomainID == domainID {
			return 0, errors.New("Alias already exists remotely")
		}
	}
	if len(aliases) >= 20 {
		return 0, errors.New("Stalwart alias slot limit exceeded")
	}
	slot := uint32(len(aliases))
	err = s.patchAlias(ctx, admin, accountID, "aliases/"+strconv.Itoa(int(slot)), map[string]any{"name": localPart, "domainId": domainID, "enable": true})
	if err != nil {
		return 0, err
	}
	return slot, nil
}
func (s *StalwartJMAPClient) SetAliasEnabled(ctx context.Context, admin StalwartCredential, accountID, domainID, localPart string, slot uint32, enabled bool) error {
	aliases, err := s.readAccountAliases(ctx, admin, accountID)
	if err != nil {
		return err
	}
	if int(slot) >= len(aliases) || aliases[slot].DomainID != domainID || aliases[slot].Name != localPart {
		return errors.New("Remote alias slot changed; reconcile manually")
	}
	return s.patchAlias(ctx, admin, accountID, fmt.Sprintf("aliases/%d/enable", slot), enabled)
}
