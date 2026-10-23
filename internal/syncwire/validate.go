package syncwire

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
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

// patterns caches compiled schema patterns. The observer validates every
// string in the configuration on every pass, and compiling each pattern per
// string was most of an idle box's sync CPU.
var patterns sync.Map // string -> *regexp.Regexp, nil when it does not compile

func pattern(expr string) *regexp.Regexp {
	if re, ok := patterns.Load(expr); ok {
		return re.(*regexp.Regexp)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		re = nil
	}
	patterns.Store(expr, re)
	return re
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
			re := pattern(s.Pattern)
			return re != nil && re.MatchString(v)
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
//
// An optional property written as its zero value — "", 0 or [] — means the
// same as leaving it out to every TOML reader on the box (voicemail = "",
// number = 0, codecs = []). Where the schema would refuse that zero value it is
// dropped instead, so sync accepts exactly the files the box's validator
// accepts rather than refusing an equivalent spelling.
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
				v = project(child, v)
				if zero(v) && !contains(s.Required, k) && !matches(child, v) {
					continue
				}
				out[k] = v
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

func zero(v any) bool {
	switch v := v.(type) {
	case string:
		return v == ""
	case float64:
		return v == 0
	case []any:
		return len(v) == 0
	}
	return false
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// Explain names the first place value fails the named schema, as a JSON path
// such as "policies[0].schedules[1].start", or "" when it matches. It names
// keys and positions only, never a value, so the answer can be logged.
func Explain(name string, value any) string {
	s, ok := schemas[name]
	if !ok {
		return name
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "(not representable as JSON)"
	}
	var plain any
	if json.Unmarshal(data, &plain) != nil {
		return "(not representable as JSON)"
	}
	if matches(s, plain) {
		return ""
	}
	path := explain(s, plain, "")
	if path == "" {
		return "(top level)"
	}
	return path
}

func explain(s schema, value any, path string) string {
	if s.Ref != "" {
		return explain(schemas[strings.TrimPrefix(s.Ref, "#/components/schemas/")], value, path)
	}
	if s.Type == "object" && s.Enum == nil && s.OneOf == nil {
		obj, ok := value.(map[string]any)
		if !ok {
			return path
		}
		for _, k := range s.Required {
			if _, ok := obj[k]; !ok {
				return join(path, k) + " (missing)"
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child, ok := s.Properties[k]
			if !ok {
				return join(path, k) + " (not allowed)"
			}
			if !matches(child, obj[k]) {
				return explain(child, obj[k], join(path, k))
			}
		}
		return path
	}
	if s.Type == "array" && s.Items != nil {
		arr, ok := value.([]any)
		if !ok || len(arr) < s.MinItems {
			return path
		}
		if len(arr) > s.MaxItems {
			return path + fmt.Sprintf(" (more than %d entries)", s.MaxItems)
		}
		for i, child := range arr {
			if !matches(*s.Items, child) {
				return explain(*s.Items, child, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	return path
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
