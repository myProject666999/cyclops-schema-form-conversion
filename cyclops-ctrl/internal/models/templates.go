package models

import (
	"github.com/cyclops-ui/cyclops/cyclops-ctrl/internal/models/helm"
	"helm.sh/helm/v3/pkg/chart"

	"github.com/cyclops-ui/cyclops/cyclops-ctrl/internal/models/dto"
)

type Template struct {
	Name            string       `json:"name"`
	Manifest        string       `json:"manifest"`
	RootField       Field        `json:"root"`
	Created         string       `json:"created"`
	Edited          string       `json:"edited"`
	Modules         []dto.Module `json:"modules"`
	Version         string       `json:"version"`
	ResolvedVersion string       `json:"resolvedVersion"`
	IconURL         string       `json:"iconURL"`

	HelmChartMetadata *helm.Metadata `json:"helmChartMetadata"`
	RawSchema         []byte         `json:"rawSchema"`

	Files     []*chart.File `json:"files"`
	Templates []*chart.File `json:"templates"`
	CRDs      []*chart.File `json:"crds"`

	Dependencies []*Template `json:"dependencies"`
	Condition    string      `json:"condition"`
}

type Field struct {
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	Type          string        `json:"type"`
	DisplayName   string        `json:"display_name"`
	ManifestKey   string        `json:"manifest_key"`
	Value         string        `json:"value"`
	Properties    []Field       `json:"properties"`
	Items         *Field        `json:"items"`
	Enum          []interface{} `json:"enum"`
	Suggestions   []interface{} `json:"x-suggestions"`
	Required      []string      `json:"required"`
	FileExtension string        `json:"fileExtension"`
	Immutable     bool          `json:"immutable"`

	// number validation
	Minimum          *float64 `json:"minimum"`
	Maximum          *float64 `json:"maximum"`
	ExclusiveMinimum *bool    `json:"exclusiveMinimum"`
	ExclusiveMaximum *bool    `json:"exclusiveMaximum"`
	MultipleOf       *float64 `json:"multipleOf"`

	// string validation
	MinLength *int    `json:"minLength"`
	MaxLength *int    `json:"maxLength"`
	Pattern   *string `json:"pattern"`

	// Variants holds the alternative shapes of a oneOf/anyOf property. The
	// frontend renders a selector over them; each variant carries its own field
	// subtree. When the schema has no discriminator the variant titles are their
	// ordinal and HasDiscriminator is false.
	Variants         []FieldVariant `json:"variants,omitempty"`
	Discriminator    string         `json:"discriminator,omitempty"`
	HasDiscriminator bool           `json:"has_discriminator,omitempty"`
	VariantNote      string         `json:"variant_note,omitempty"`
	// Condition, when set, tells the frontend to show this field only when the
	// referenced field equals the given constant (or, for dependentRequired, is
	// present at all).
	Condition *FieldCondition `json:"condition,omitempty"`
	// ReadOnly is set for a field whose value cannot be edited, e.g. a
	// single-valued enum or a const. The field stays visible.
	ReadOnly bool `json:"read_only,omitempty"`
}

// FieldVariant is one branch of a oneOf/anyOf composition.
type FieldVariant struct {
	Title  string `json:"title"`
	Fields Field  `json:"fields"`
}

// FieldCondition expresses a single supported dependency form. When Required is
// false the field is shown only while the condition holds; when Required is true
// the field stays visible but is mandatory only while the condition holds. The
// condition is "the field at Path equals Value" or, when Present is true, "the
// field at Path is set". Richer if/then/else expressions are not converted.
type FieldCondition struct {
	Path string `json:"path"`
	// Value has no omitempty so a constant false or 0 survives serialisation.
	Value    interface{} `json:"value"`
	Present  bool        `json:"present,omitempty"`
	Required bool        `json:"required,omitempty"`
}
