package resource

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

// Decode requires an object, rejects ambiguous duplicate keys and nulls at any
// depth, then enforces the concrete type's field allowlist. Limits are checked
// before parsing; this is not a permissive generic resource property map.
func Decode(data []byte, limit int, out any) error {
	if len(data) == 0 || len(data) > limit {
		return errors.New("invalid resource JSON size")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("resource JSON must be an object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := value(d, 0, reflect.TypeOf(out)); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("unexpected resource JSON data")
	}
	typed := json.NewDecoder(bytes.NewReader(data))
	typed.DisallowUnknownFields()
	return typed.Decode(out)
}
func value(d *json.Decoder, depth int, shape reflect.Type) error {
	for shape != nil && shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	if depth > 12 {
		return errors.New("resource JSON is too deeply nested")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	if t == nil {
		return errors.New("resource JSON cannot contain null")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			t, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := t.(string)
			if !ok || seen[key] {
				return errors.New("duplicate resource JSON field")
			}
			seen[key] = true
			child := fieldType(shape, key)
			if shape != nil && shape.Kind() == reflect.Struct && child == nil {
				return errors.New("unknown resource JSON field")
			}
			if err := value(d, depth+1, child); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			var element reflect.Type
			if shape != nil && (shape.Kind() == reflect.Slice || shape.Kind() == reflect.Array) {
				element = shape.Elem()
			}
			if err := value(d, depth+1, element); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid resource JSON delimiter")
	}
	_, err = d.Token()
	return err
}

// encoding/json accepts case-insensitive aliases for struct fields. The wire
// contract does not: exact tags prevent two spellings overwriting one field.
func fieldType(shape reflect.Type, key string) reflect.Type {
	if shape == nil || shape.Kind() != reflect.Struct {
		return nil
	}
	for i := 0; i < shape.NumField(); i++ {
		field := shape.Field(i)
		if field.Anonymous {
			if found := fieldType(field.Type, key); found != nil {
				return found
			}
		}
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == key && tag != "" && tag != "-" {
			return field.Type
		}
	}
	return nil
}
