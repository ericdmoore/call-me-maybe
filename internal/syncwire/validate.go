package syncwire

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf16"
)

//go:embed openapi.json
var document []byte

var ErrInvalid = errors.New("sync: invalid protocol value")

type schema struct {
	Ref        string            `json:"$ref"`
	Type       string            `json:"type"`
	Enum       []any             `json:"enum"`
	OneOf      []schema          `json:"oneOf"`
	Properties map[string]schema `json:"properties"`
	Required   []string          `json:"required"`
	Items      *schema           `json:"items"`
	MinItems   int               `json:"minItems"`
	MaxItems   int               `json:"maxItems"`
	MaxLength  int               `json:"maxLength"`
	Pattern    string            `json:"pattern"`
	Minimum    float64           `json:"minimum"`
	Maximum    float64           `json:"maximum"`
}

var schemas = func() map[string]schema {
	var doc struct {
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if json.Unmarshal(document, &doc) != nil {
		panic("invalid embedded sync schema")
	}
	return doc.Components.Schemas
}()

// Decode uses the same closed, bounded schema as the hosted boundary. Generated
// Go structs alone do not reject missing properties, unknown fields or bad enums.
func Decode(name string, data []byte, target any) error {
	if len(data) > 2<<20 {
		return ErrInvalid
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(data))
	if d.Decode(&value) != nil {
		return ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	s, ok := schemas[name]
	if !ok || !matches(s, value) {
		return ErrInvalid
	}
	if json.Unmarshal(data, target) != nil {
		return ErrInvalid
	}
	return nil
}

func matches(s schema, value any) bool {
	if s.Ref != "" {
		return matches(schemas[strings.TrimPrefix(s.Ref, "#/components/schemas/")], value)
	}
	if s.Enum != nil {
		for _, v := range s.Enum {
			if reflect.DeepEqual(v, value) {
				return true
			}
		}
		return false
	}
	if s.OneOf != nil {
		count := 0
		for _, option := range s.OneOf {
			if matches(option, value) {
				count++
			}
		}
		return count == 1
	}
	switch s.Type {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, k := range s.Required {
			if _, ok := obj[k]; !ok {
				return false
			}
		}
		for k, v := range obj {
			child, ok := s.Properties[k]
			if !ok || !matches(child, v) {
				return false
			}
		}
		return true
	case "array":
		arr, ok := value.([]any)
		if !ok || len(arr) < s.MinItems || len(arr) > s.MaxItems || s.Items == nil {
			return false
		}
		for _, child := range arr {
			if !matches(*s.Items, child) {
				return false
			}
		}
		return true
	case "string":
		v, ok := value.(string)
		if !ok || len(utf16.Encode([]rune(v))) > s.MaxLength {
			return false
		}
		if s.Pattern != "" {
			match, err := regexp.MatchString(s.Pattern, v)
			return err == nil && match
		}
		return true
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer", "number":
		v, ok := value.(float64)
		return ok && v >= s.Minimum && v <= s.Maximum && !math.IsNaN(v) && !math.IsInf(v, 0) && (s.Type != "integer" || math.Trunc(v) == v)
	}
	return false
}

// Project accepts only the schema's explicitly public properties. Callers first
// replace credential values with presence flags; new local fields cannot leak
// into snapshots merely because they were added to a TOML file.
func Project(name string, value any) (any, error) {
	s, ok := schemas[name]
	if !ok {
		return nil, ErrInvalid
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	var plain any
	if json.Unmarshal(data, &plain) != nil {
		return nil, ErrInvalid
	}
	plain = project(s, plain)
	if !matches(s, plain) {
		return nil, ErrInvalid
	}
	return plain, nil
}

func project(s schema, value any) any {
	if s.Ref != "" {
		return project(schemas[strings.TrimPrefix(s.Ref, "#/components/schemas/")], value)
	}
	if obj, ok := value.(map[string]any); ok && s.Type == "object" {
		out := map[string]any{}
		for k, v := range obj {
			if child, ok := s.Properties[k]; ok {
				out[k] = project(child, v)
			}
		}
		return out
	}
	if arr, ok := value.([]any); ok && s.Type == "array" && s.Items != nil {
		out := make([]any, len(arr))
		for i, v := range arr {
			out[i] = project(*s.Items, v)
		}
		return out
	}
	return value
}
