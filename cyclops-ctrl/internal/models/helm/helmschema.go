package helm

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"strconv"

	json "github.com/json-iterator/go"
)

type Property struct {
	Title         string              `json:"title"`
	Type          PropertyType        `json:"type"`
	Description   string              `json:"description"`
	Order         []string            `json:"order"`
	Properties    map[string]Property `json:"properties"`
	Items         *Property           `json:"items"`
	Enum          []interface{}       `json:"enum"`
	Suggestions   []interface{}       `json:"x-suggestions"`
	Required      []string            `json:"required"`
	FileExtension string              `json:"fileExtension"`
	Reference     string              `json:"$ref"`
	Definitions   map[string]Property `json:"$defs"`
	// LegacyDefinitions holds draft-7 `definitions`. It is kept separate from
	// Definitions so both reference spellings can be resolved.
	LegacyDefinitions map[string]Property `json:"definitions"`
	Immutable         bool                `json:"immutable"`

	// number validation
	Minimum          *float64       `json:"minimum"`
	Maximum          *float64       `json:"maximum"`
	ExclusiveMinimum ExclusiveBound `json:"exclusiveMinimum"`
	ExclusiveMaximum ExclusiveBound `json:"exclusiveMaximum"`
	MultipleOf       *float64       `json:"multipleOf"`

	// string validation
	MinLength *int    `json:"minLength"`
	MaxLength *int    `json:"maxLength"`
	Pattern   *string `json:"pattern"`

	// schema compositions
	AnyOf []Property `json:"anyOf"`
	OneOf []Property `json:"oneOf"`
	AllOf []Property `json:"allOf"`

	// value constraints
	Const *RawValue `json:"const"`

	// conditionals
	If   *Property `json:"if"`
	Then *Property `json:"then"`
	Else *Property `json:"else"`

	// property dependencies
	DependentRequired map[string][]string  `json:"dependentRequired"`
	Dependencies      map[string]*RawValue `json:"dependencies"`

	// keywords that are recognised only so that they can be reported as ignored
	Not                   *RawValue           `json:"not"`
	PatternProperties     map[string]Property `json:"patternProperties"`
	PrefixItems           []*RawValue         `json:"prefixItems"`
	Contains              *RawValue           `json:"contains"`
	AdditionalProperties  *RawValue           `json:"additionalProperties"`
	PropertyNames         *RawValue           `json:"propertyNames"`
	UnevaluatedProperties *RawValue           `json:"unevaluatedProperties"`
	UnevaluatedItems      *RawValue           `json:"unevaluatedItems"`
}

type PropertyType string

func (t *PropertyType) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as a single string
	var singleString string
	if err := json.Unmarshal(data, &singleString); err == nil {
		*t = PropertyType(singleString)
		return nil
	}

	// Try to unmarshal as a list of strings
	var stringList []string
	if err := json.Unmarshal(data, &stringList); err != nil {
		return fmt.Errorf("data is neither a single string nor a list of strings")
	}

	unique := make(map[string]struct{})
	for _, s := range stringList {
		unique[s] = struct{}{}
	}

	delete(unique, "null")

	if len(unique) == 1 {
		for s := range unique {
			*t = PropertyType(s)
			return nil
		}
	}

	delete(unique, "string")

	if len(unique) == 1 {
		for s := range unique {
			*t = PropertyType(s)
			return nil
		}
	}

	if _, ok := unique["object"]; ok {
		*t = "object"
		return nil
	}

	return fmt.Errorf("cant resolve %v", string(data))
}

func (p Property) HasRef() bool {
	return len(p.Reference) > 0
}

// ExclusiveBound represents the JSON Schema exclusiveMinimum/exclusiveMaximum
// keyword. Draft 4 encodes it as a boolean modifying minimum/maximum; draft 6
// and later encode it as a number that is a bound on its own. Both spellings
// are accepted so that schemas of either draft can be parsed.
type ExclusiveBound struct {
	Set      bool
	IsNumber bool
	Bool     bool
	Number   float64
}

func (e *ExclusiveBound) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*e = ExclusiveBound{}
		return nil
	}

	if bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false")) {
		*e = ExclusiveBound{Set: true, Bool: trimmed[0] == 't'}
		return nil
	}

	var number float64
	if err := stdjson.Unmarshal(trimmed, &number); err != nil {
		return fmt.Errorf("exclusive bound must be a boolean or a number, got %s", string(trimmed))
	}

	*e = ExclusiveBound{Set: true, IsNumber: true, Number: number}
	return nil
}

func (e ExclusiveBound) MarshalJSON() ([]byte, error) {
	if !e.Set {
		return []byte("null"), nil
	}

	if e.IsNumber {
		return []byte(strconv.FormatFloat(e.Number, 'f', -1, 64)), nil
	}

	return []byte(strconv.FormatBool(e.Bool)), nil
}

// ApplyMinimum combines the keyword with the inclusive lower bound it may
// modify. It returns the effective inclusive bound and, when the keyword
// constrains it, the legacy boolean representation used by the frontend field
// contract.
func (e ExclusiveBound) ApplyMinimum(inclusive *float64) (*float64, *bool) {
	if !e.Set {
		return inclusive, nil
	}

	if e.IsNumber {
		bound := e.Number
		exclusive := true
		// x > e.Number combined with x >= minimum. If minimum is the larger
		// bound the effective constraint is inclusive minimum.
		if inclusive != nil && *inclusive > bound {
			bound = *inclusive
			exclusive = false
		}
		return &bound, &exclusive
	}

	exclusive := e.Bool
	return inclusive, &exclusive
}

// ApplyMaximum is the upper-bound counterpart of ApplyMinimum.
func (e ExclusiveBound) ApplyMaximum(inclusive *float64) (*float64, *bool) {
	if !e.Set {
		return inclusive, nil
	}

	if e.IsNumber {
		bound := e.Number
		exclusive := true
		// x < e.Number combined with x <= maximum.
		if inclusive != nil && *inclusive < bound {
			bound = *inclusive
			exclusive = false
		}
		return &bound, &exclusive
	}

	exclusive := e.Bool
	return inclusive, &exclusive
}

// RawValue keeps a JSON value verbatim so keyword-specific decoding can happen
// later without forcing Property to implement a full custom unmarshaler.
type RawValue struct {
	Raw []byte
}

func (r *RawValue) UnmarshalJSON(data []byte) error {
	r.Raw = append(r.Raw[:0], data...)
	return nil
}

func (r RawValue) MarshalJSON() ([]byte, error) {
	if len(r.Raw) == 0 {
		return []byte("null"), nil
	}

	return r.Raw, nil
}

// Decode unmarshals the captured value into dst.
func (r *RawValue) Decode(dst interface{}) error {
	if r == nil || len(r.Raw) == 0 {
		return fmt.Errorf("no value captured")
	}

	return stdjson.Unmarshal(r.Raw, dst)
}

// IsBool reports whether the captured value is the literal true or false.
func (r *RawValue) IsBool() (bool, bool) {
	if r == nil {
		return false, false
	}

	switch string(bytes.TrimSpace(r.Raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// ScalarValue returns the captured value when it is a JSON string, number or
// boolean. Objects and arrays are not treated as discriminators.
func (r *RawValue) ScalarValue() (interface{}, bool) {
	if r == nil {
		return nil, false
	}

	var value interface{}
	if err := r.Decode(&value); err != nil {
		return nil, false
	}

	switch value.(type) {
	case string, float64, bool:
		return value, true
	default:
		return nil, false
	}
}
