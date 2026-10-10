package resourcecatalog

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// DecodeSnapshot accepts only this local aggregate schema, never a peer reply.
// All non-optional fields are required. Unknown/case-variant/duplicate fields,
// null, trailing values, invalid UTF-8 and excessive nesting fail closed.
func DecodeSnapshot(data []byte, l Limits) (Snapshot, error) {
	var v Snapshot
	if l.Validate() != nil || decodeClosed(data, l.MaxBytes, l, &v) != nil || v.Validate(l) != nil {
		return Snapshot{}, ErrInvalid
	}
	return v, nil
}
func EncodeSnapshot(v Snapshot, l Limits) ([]byte, error) {
	if v.Validate(l) != nil {
		return nil, ErrInvalid
	}
	return encodeBounded(v, l.MaxBytes, l)
}
func DecodeResult(data []byte, l Limits) (HistoricalResult, error) {
	var v HistoricalResult
	if l.Validate() != nil || decodeClosed(data, l.MaxPageBytes, l, &v) != nil || v.Validate(l) != nil {
		return HistoricalResult{}, ErrInvalid
	}
	return v, nil
}

func decodeClosed(data []byte, limit int, l Limits, out any) error {
	if len(data) == 0 || len(data) > limit || !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if tokenValue(d, reflect.TypeOf(out), 0, l) != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrInvalid
	}
	return nil
}

type fieldShape struct {
	kind     reflect.Type
	optional bool
}

func fields(t reflect.Type) map[string]fieldShape {
	out := map[string]fieldShape{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			for key, shape := range fields(f.Type) {
				out[key] = shape
			}
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")
		if tag[0] == "" || tag[0] == "-" {
			continue
		}
		optional := false
		for _, flag := range tag[1:] {
			optional = optional || flag == "omitempty"
		}
		out[tag[0]] = fieldShape{f.Type, optional}
	}
	return out
}
func tokenValue(d *json.Decoder, t reflect.Type, depth int, l Limits) error {
	if depth > 12 {
		return ErrInvalid
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return ErrInvalid
	}
	switch t.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return ErrInvalid
		}
		allowed, seen := fields(t), map[string]bool{}
		for d.More() {
			keyToken, err := d.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return ErrInvalid
			}
			shape, ok := allowed[key]
			if !ok {
				return ErrInvalid
			}
			seen[key] = true
			if tokenValue(d, shape.kind, depth+1, l) != nil {
				return ErrInvalid
			}
		}
		if token, err := d.Token(); err != nil || token != json.Delim('}') {
			return ErrInvalid
		}
		for key, shape := range allowed {
			if !shape.optional && !seen[key] {
				return ErrInvalid
			}
		}
	case reflect.Slice:
		if token != json.Delim('[') {
			return ErrInvalid
		}
		count := 0
		for d.More() {
			count++
			if count > collectionLimit(l) || tokenValue(d, t.Elem(), depth+1, l) != nil {
				return ErrInvalid
			}
		}
		if token, err := d.Token(); err != nil || token != json.Delim(']') {
			return ErrInvalid
		}
	case reflect.String:
		value, ok := token.(string)
		if !ok || len(value) > l.MaxStringBytes {
			return ErrInvalid
		}
	case reflect.Bool:
		if _, ok := token.(bool); !ok {
			return ErrInvalid
		}
	case reflect.Int, reflect.Int64, reflect.Uint64:
		if _, ok := token.(json.Number); !ok {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func collectionLimit(l Limits) int {
	limit := l.MaxRows
	if l.MaxSources > limit {
		limit = l.MaxSources
	}
	if limit < 5 {
		limit = 5
	}
	return limit
}

// Bound strings/collections before marshaling. JSON escaping can expand strings
// by at most a fixed factor; the exact serialized bound is checked afterwards.
func encodeBounded(v any, limit int, l Limits) ([]byte, error) {
	remaining := limit
	if preflight(reflect.ValueOf(v), 0, l, &remaining) != nil {
		return nil, ErrLimited
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(data) > limit {
		return nil, ErrLimited
	}
	return data, nil
}
func preflight(v reflect.Value, depth int, l Limits, remaining *int) error {
	if depth > 12 {
		return ErrInvalid
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return preflight(v.Elem(), depth, l, remaining)
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if preflight(v.Field(i), depth+1, l, remaining) != nil {
				return ErrLimited
			}
		}
	case reflect.Slice:
		if v.Len() > collectionLimit(l) {
			return ErrLimited
		}
		for i := 0; i < v.Len(); i++ {
			if preflight(v.Index(i), depth+1, l, remaining) != nil {
				return ErrLimited
			}
		}
	case reflect.String:
		if v.Len() > l.MaxStringBytes || v.Len() > *remaining || !utf8.ValidString(v.String()) {
			return ErrLimited
		}
		*remaining -= v.Len()
	case reflect.Bool, reflect.Int, reflect.Int64, reflect.Uint64:
	default:
		return ErrInvalid
	}
	return nil
}
func equalRow(a, b Row) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func DecodeResultObservation(data []byte, l Limits) (ResultObservation, error) {
	var v ResultObservation
	if l.Validate() != nil || decodeClosed(data, l.MaxPageBytes, l, &v) != nil || v.Validate(l) != nil {
		return ResultObservation{}, ErrInvalid
	}
	return v, nil
}
