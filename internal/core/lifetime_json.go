package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// lifetimeFields preserves presence information: a null or duplicated deadline
// must never turn into a zero-value timestamp and an indefinite permission.
func lifetimeFields(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("JSON object required")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid object key")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, errors.New("duplicate JSON field")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("unexpected trailing JSON")
	}
	return fields, nil
}

func validateLifetimeInput(raw []byte) error {
	fields, err := lifetimeFields(raw)
	if err != nil {
		return err
	}
	for _, name := range []string{"lifetime", "ttlSeconds", "runtimeLifetime", "runtimeTTLSeconds"} {
		if value, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("lifetime fields cannot be null")
		}
	}
	return nil
}

func (s *RemoteService) UnmarshalJSON(raw []byte) error {
	fields, err := lifetimeFields(raw)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"id": true, "purpose": true, "network": true, "ports": true, "expiresAt": true, "lifetime": true, "application": true}
	for key, value := range fields {
		if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("unknown or null service metadata")
		}
	}
	type wire RemoteService
	var value wire
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.Purpose == "" {
		value.Purpose = "generic"
	}
	_, hasExpiry := fields["expiresAt"]
	if value.Lifetime == "until-revoked" {
		if hasExpiry {
			return errors.New("until-revoked metadata must omit expiry")
		}
	} else if !hasExpiry {
		return errors.New("finite service metadata requires expiry")
	}
	*s = RemoteService(value)
	return validateRemote(*s)
}
