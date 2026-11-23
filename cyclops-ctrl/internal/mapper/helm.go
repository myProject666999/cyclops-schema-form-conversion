package mapper

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cyclops-ui/cyclops/cyclops-ctrl/internal/models"
	"github.com/cyclops-ui/cyclops/cyclops-ctrl/internal/models/helm"
)

// maxConversionDepth bounds how deep a single conversion may recurse while
// following nested properties, array items, composition branches and $ref
// chains. Recursive schemas (a tree referencing itself) would otherwise recurse
// until the process runs out of stack.
const maxConversionDepth = 16

// DiagnosticLevel marks whether a diagnostic prevents the affected part of the
// form from being rendered faithfully or only records a keyword that was
// deliberately ignored.
type DiagnosticLevel string

const (
	// DiagnosticWarning reports a keyword that was ignored or a subtree that was
	// truncated. The rest of the field tree is still usable.
	DiagnosticWarning DiagnosticLevel = "warning"
	// DiagnosticError reports something that makes the produced field lossy,
	// e.g. an unresolvable $ref or a conflicting allOf merge.
	DiagnosticError DiagnosticLevel = "error"
)

// Diagnostic reports a problem found while converting a Helm values schema into
// frontend fields. Path identifies the offending schema location using the same
// dotted path the converter walks (e.g. "root.storage.items").
type Diagnostic struct {
	Level   DiagnosticLevel `json:"level"`
	Path    string          `json:"path"`
	Keyword string          `json:"keyword"`
	Message string          `json:"message"`
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s: %s: %s", d.Level, d.Path, d.Keyword, d.Message)
}

// HelmSchemaToFields converts a Helm values schema into the field tree consumed
// by the frontend. It keeps the historical signature; callers that want the
// conversion diagnostics should use HelmSchemaToFieldsWithDiagnostics.
func HelmSchemaToFields(name string, schema helm.Property, defs map[string]helm.Property, dependencies []*models.Template) models.Field {
	field, _ := HelmSchemaToFieldsWithDiagnostics(name, schema, defs, dependencies)
	return field
}

// HelmSchemaToFieldsWithDiagnostics converts a Helm values schema into the
// field tree consumed by the frontend and returns every diagnostic gathered on
// the way. Diagnostics are ordered deterministically so repeated conversions of
// the same schema produce byte-identical results.
func HelmSchemaToFieldsWithDiagnostics(name string, schema helm.Property, defs map[string]helm.Property, dependencies []*models.Template) (models.Field, []Diagnostic) {
	c := &converter{
		defs:       defs,
		legacyDefs: schema.LegacyDefinitions,
	}

	field := c.convert(name, name, schema, 0, dependencies)
	c.sortDiagnostics()

	return field, c.diagnostics
}

type converter struct {
	defs        map[string]helm.Property
	legacyDefs  map[string]helm.Property
	diagnostics []Diagnostic
}

func (c *converter) report(level DiagnosticLevel, path, keyword, message string) {
	c.diagnostics = append(c.diagnostics, Diagnostic{
		Level:   level,
		Path:    path,
		Keyword: keyword,
		Message: message,
	})
}

func (c *converter) sortDiagnostics() {
	sort.SliceStable(c.diagnostics, func(i, j int) bool {
		a, b := c.diagnostics[i], c.diagnostics[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Keyword != b.Keyword {
			return a.Keyword < b.Keyword
		}
		if a.Message != b.Message {
			return a.Message < b.Message
		}
		return a.Level < b.Level
	})
}

func (c *converter) convert(name, path string, schema helm.Property, depth int, dependencies []*models.Template) models.Field {
	if depth > maxConversionDepth {
		c.report(DiagnosticWarning, path, "$ref", fmt.Sprintf("maximum conversion depth of %d exceeded; subtree truncated", maxConversionDepth))
		field := c.baseField(name, schema, path)
		if len(field.Type) == 0 {
			field.Type = "object"
		}
		return field
	}

	if schema.HasRef() {
		resolved, ok := c.resolveRef(schema.Reference, path)
		if !ok {
			field := c.baseField(name, schema, path)
			if len(field.Type) == 0 {
				field.Type = "object"
			}
			return field
		}

		return c.convert(name, path, resolved, depth+1, dependencies)
	}

	if len(schema.AllOf) > 0 {
		schema = c.mergeAllOf(schema, path)
	}

	c.reportIgnoredKeywords(schema, path)

	if len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 {
		return c.compositionField(name, path, schema, depth, dependencies)
	}

	if schema.Type == "array" || (schema.Type == "" && schema.Items != nil && len(schema.Properties) == 0) {
		return c.arrayField(name, path, schema, depth)
	}

	return c.objectField(name, path, schema, depth, dependencies)
}

// baseField builds a field carrying every leaf-level attribute of the schema.
// Structure (properties, items, variants) is added by the callers.
func (c *converter) baseField(name string, schema helm.Property, path string) models.Field {
	minimum, exclusiveMinimum := schema.ExclusiveMinimum.ApplyMinimum(schema.Minimum)
	maximum, exclusiveMaximum := schema.ExclusiveMaximum.ApplyMaximum(schema.Maximum)

	fieldType := mapHelmPropertyTypeToFieldType(schema)
	if len(fieldType) == 0 {
		fieldType = inferScalarType(schema)
	}

	return models.Field{
		Name:             name,
		Description:      schema.Description,
		Type:             fieldType,
		DisplayName:      mapTitle(name, schema.Title),
		ManifestKey:      name,
		Enum:             schema.Enum,
		Suggestions:      schema.Suggestions,
		Required:         schema.Required,
		FileExtension:    schema.FileExtension,
		Minimum:          minimum,
		Maximum:          maximum,
		ExclusiveMinimum: exclusiveMinimum,
		ExclusiveMaximum: exclusiveMaximum,
		MultipleOf:       schema.MultipleOf,
		MinLength:        schema.MinLength,
		MaxLength:        schema.MaxLength,
		Pattern:          schema.Pattern,
		Immutable:        schema.Immutable,
		// A single-valued enum or a const has only one valid value. It is kept
		// visible (so the user can see what the value is) but rendered read-only.
		ReadOnly: len(schema.Enum) == 1 || schema.Const != nil,
	}
}

func inferScalarType(schema helm.Property) string {
	if schema.Const != nil {
		if value, ok := schema.Const.ScalarValue(); ok {
			switch value.(type) {
			case string:
				return "string"
			case float64:
				return "number"
			case bool:
				return "boolean"
			}
		}
	}

	if len(schema.Enum) == 1 {
		switch schema.Enum[0].(type) {
		case string:
			return "string"
		case float64:
			return "number"
		case bool:
			return "boolean"
		}
	}

	return ""
}

func (c *converter) arrayField(name, path string, schema helm.Property, depth int) models.Field {
	field := c.baseField(name, schema, path)
	field.Type = "array"
	field.Items = c.arrayItem(schema.Items, path+".items", depth+1)
	return field
}

func (c *converter) arrayItem(item *helm.Property, path string, depth int) *models.Field {
	if item == nil {
		return nil
	}

	// Preserve the historical behaviour: an item without a type and without a
	// reference is rendered as a string. Once a $ref is present the reference is
	// resolved first and the resulting type decides.
	if len(item.Type) == 0 && !item.HasRef() {
		return &models.Field{Type: "string"}
	}

	if depth > maxConversionDepth {
		c.report(DiagnosticWarning, path, "$ref", fmt.Sprintf("maximum conversion depth of %d exceeded; array item truncated", maxConversionDepth))
		return &models.Field{Type: "string"}
	}

	field := c.convert("", path, *item, depth, nil)
	if len(field.Type) == 0 {
		field.Type = "string"
	}

	return &field
}

func (c *converter) objectField(name, path string, schema helm.Property, depth int, dependencies []*models.Template) models.Field {
	uniqueFieldNames := make(map[string]struct{})
	fields := make([]models.Field, 0, len(schema.Properties))
	// Iterate in sorted order so the produced tree does not depend on Go's map
	// ordering, which keeps repeated conversions byte-identical.
	for _, propertyName := range sortedKeys(schema.Properties) {
		uniqueFieldNames[propertyName] = struct{}{}
		property := schema.Properties[propertyName]
		fields = append(fields, c.convert(propertyName, path+"."+propertyName, property, depth+1, nil))
	}

	fields = c.conditionalFields(fields, path, schema, depth)

	fields = sortFields(fields, schema.Order)

	c.applyDependentRequired(fields, schema, path)

	for _, dependency := range dependencies {
		// if the dependency schema is already present in the root schema, skip using schema from dependency
		if _, ok := uniqueFieldNames[dependency.Name]; ok {
			continue
		}

		if dependency.RootField.Type != "object" {
			continue
		}

		fields = append(fields, models.Field{
			Name:             dependency.Name,
			Description:      dependency.RootField.Description,
			Type:             dependency.RootField.Type,
			DisplayName:      mapTitle(dependency.Name, dependency.RootField.DisplayName),
			ManifestKey:      dependency.RootField.ManifestKey,
			Value:            dependency.RootField.Value,
			Properties:       dependency.RootField.Properties,
			Items:            dependency.RootField.Items,
			Enum:             dependency.RootField.Enum,
			Suggestions:      dependency.RootField.Suggestions,
			Required:         dependency.RootField.Required,
			FileExtension:    dependency.RootField.FileExtension,
			Minimum:          dependency.RootField.Minimum,
			Maximum:          dependency.RootField.Maximum,
			MultipleOf:       dependency.RootField.MultipleOf,
			ExclusiveMinimum: dependency.RootField.ExclusiveMinimum,
			ExclusiveMaximum: dependency.RootField.ExclusiveMaximum,
			MinLength:        dependency.RootField.MinLength,
			MaxLength:        dependency.RootField.MaxLength,
			Pattern:          dependency.RootField.Pattern,
			Immutable:        dependency.RootField.Immutable,
			Variants:         dependency.RootField.Variants,
			Discriminator:    dependency.RootField.Discriminator,
			HasDiscriminator: dependency.RootField.HasDiscriminator,
			VariantNote:      dependency.RootField.VariantNote,
			Condition:        dependency.RootField.Condition,
			ReadOnly:         dependency.RootField.ReadOnly,
		})
	}

	field := c.baseField(name, schema, path)
	field.Properties = fields
	return field
}

// conditionalFields turns if/then/else into display conditions. Only the
// "sibling field equals a constant" / "sibling field is present" forms are
// representable; any other form is reported and the affected fields are kept
// visible so a user is never misled into thinking a conditional field is always
// shown.
func (c *converter) conditionalFields(fields []models.Field, path string, schema helm.Property, depth int) []models.Field {
	if schema.If == nil {
		if schema.Then != nil || schema.Else != nil {
			c.report(DiagnosticWarning, path, "then/else", "then/else without if ignored")
		}
		return fields
	}

	condition, ok := extractCondition(*schema.If)
	if !ok {
		c.report(DiagnosticWarning, path+".if", "if", "conditional expression is not a single equality; if/then/else not converted")
	}

	if schema.Then != nil {
		then := c.resolveNested(*schema.Then, path+".then")

		for _, propertyName := range sortedKeys(then.Properties) {
			if index := fieldIndex(fields, propertyName); index >= 0 {
				c.report(DiagnosticWarning, path+"."+propertyName, "then", "conditional constraint on an already declared property cannot be represented as a display condition; the field stays visible")
				continue
			}

			field := c.convert(propertyName, path+"."+propertyName, then.Properties[propertyName], depth+1, nil)
			if ok {
				field.Condition = cloneCondition(condition)
			}
			fields = append(fields, field)
		}

		for _, requiredName := range then.Required {
			index := fieldIndex(fields, requiredName)
			if index < 0 || !ok {
				c.report(DiagnosticWarning, path+".then", "then", fmt.Sprintf("conditional required property %q cannot be represented", requiredName))
				continue
			}

			conditional := cloneCondition(condition)
			conditional.Required = true
			fields[index].Condition = conditional
		}
	}

	if schema.Else != nil {
		c.report(DiagnosticWarning, path+".else", "else", "negated conditional is not represented; fields kept visible")
		els := c.resolveNested(*schema.Else, path+".else")
		for _, propertyName := range sortedKeys(els.Properties) {
			if fieldIndex(fields, propertyName) >= 0 {
				c.report(DiagnosticWarning, path+"."+propertyName, "else", "conditional constraint on an already declared property cannot be represented as a display condition; the field stays visible")
				continue
			}
			fields = append(fields, c.convert(propertyName, path+"."+propertyName, els.Properties[propertyName], depth+1, nil))
		}
	}

	return fields
}

func (c *converter) resolveNested(property helm.Property, path string) helm.Property {
	if !property.HasRef() {
		return property
	}

	resolved, ok := c.resolveRef(property.Reference, path)
	if !ok {
		return helm.Property{}
	}

	return resolved
}

// extractCondition recognises the supported display-condition shapes:
//   - {"properties": {"x": {"const": v}}} or a single-valued enum -> x == v
//   - {"required": ["x"]} -> x is present
func extractCondition(ifSchema helm.Property) (*models.FieldCondition, bool) {
	if len(ifSchema.Properties) == 1 {
		for propertyName, property := range ifSchema.Properties {
			if property.Const != nil {
				if value, ok := property.Const.ScalarValue(); ok {
					return &models.FieldCondition{Path: propertyName, Value: value}, true
				}
			}

			if len(property.Enum) == 1 {
				return &models.FieldCondition{Path: propertyName, Value: property.Enum[0]}, true
			}
		}
	}

	if len(ifSchema.Required) == 1 && len(ifSchema.Properties) == 0 {
		return &models.FieldCondition{Path: ifSchema.Required[0], Present: true}, true
	}

	return nil, false
}

func cloneCondition(condition *models.FieldCondition) *models.FieldCondition {
	if condition == nil {
		return nil
	}

	copy := *condition
	return &copy
}

// applyDependentRequired attaches a "mandatory only while the named sibling is
// present" condition to every property listed by dependentRequired.
func (c *converter) applyDependentRequired(fields []models.Field, schema helm.Property, path string) {
	apply := func(dependentField string, requiredFields []string) {
		for _, requiredField := range requiredFields {
			index := fieldIndex(fields, requiredField)
			if index < 0 {
				c.report(DiagnosticWarning, path+".dependentRequired", "dependentRequired", fmt.Sprintf("dependent property %q not found in sibling properties", requiredField))
				continue
			}

			if fields[index].Condition != nil {
				c.report(DiagnosticWarning, path+"."+requiredField, "dependentRequired", "field already has a display condition; dependentRequired not applied")
				continue
			}

			fields[index].Condition = &models.FieldCondition{Path: dependentField, Present: true, Required: true}
		}
	}

	for _, dependentField := range sortedKeys(schema.DependentRequired) {
		apply(dependentField, schema.DependentRequired[dependentField])
	}

	// The draft-7 `dependencies` keyword has an array form equivalent to
	// dependentRequired; its object form is a dependent schema, which is not
	// representable and is reported separately.
	for _, dependentField := range sortedKeys(schema.Dependencies) {
		dependency := schema.Dependencies[dependentField]
		if dependency == nil {
			continue
		}

		var names []string
		if err := dependency.Decode(&names); err != nil {
			continue
		}

		apply(dependentField, names)
	}
}

func fieldIndex(fields []models.Field, name string) int {
	for index := range fields {
		if fields[index].Name == name {
			return index
		}
	}

	return -1
}

// reportIgnoredKeywords records every keyword that the converter intentionally
// does not translate, so a user never mistakes an unconstrained field for a
// faithful rendering of the schema.
func (c *converter) reportIgnoredKeywords(schema helm.Property, path string) {
	if schema.Not != nil {
		c.report(DiagnosticWarning, path, "not", "keyword is not supported and was ignored")
	}

	if len(schema.PatternProperties) > 0 {
		c.report(DiagnosticWarning, path, "patternProperties", "keyword is not supported and was ignored")
	}

	if len(schema.PrefixItems) > 0 {
		c.report(DiagnosticWarning, path, "prefixItems", "keyword is not supported and was ignored")
	}

	if schema.Contains != nil {
		c.report(DiagnosticWarning, path, "contains", "keyword is not supported and was ignored")
	}

	if schema.PropertyNames != nil {
		c.report(DiagnosticWarning, path, "propertyNames", "keyword is not supported and was ignored")
	}

	if schema.UnevaluatedProperties != nil {
		if _, isBool := schema.UnevaluatedProperties.IsBool(); !isBool {
			c.report(DiagnosticWarning, path, "unevaluatedProperties", "keyword is not supported and was ignored")
		}
	}

	if schema.UnevaluatedItems != nil {
		if _, isBool := schema.UnevaluatedItems.IsBool(); !isBool {
			c.report(DiagnosticWarning, path, "unevaluatedItems", "keyword is not supported and was ignored")
		}
	}

	if schema.AdditionalProperties != nil {
		if _, isBool := schema.AdditionalProperties.IsBool(); !isBool {
			c.report(DiagnosticWarning, path, "additionalProperties", "schema-valued additionalProperties is not supported and was ignored")
		}
	}

	for _, dependentField := range sortedKeys(schema.Dependencies) {
		dependency := schema.Dependencies[dependentField]
		if dependency == nil {
			continue
		}

		var names []string
		if err := dependency.Decode(&names); err != nil {
			c.report(DiagnosticWarning, path+".dependencies", "dependencies", fmt.Sprintf("schema-form dependency for %q is not supported and was ignored", dependentField))
		}
	}
}

// resolveRef resolves a JSON Schema $ref. Both the draft 2020-12 "#/$defs/..."
// and the draft 7 "#/definitions/..." spellings are accepted. Beyond the
// registry prefix the path is walked level by level, preferring a node's $defs
// (then its legacy definitions) over its properties, so nested paths such as
// "#/$defs/a/b" are resolved inside the definitions tree rather than silently
// switching to properties. An unresolvable reference produces a diagnostic with
// the schema path.
func (c *converter) resolveRef(reference, path string) (helm.Property, bool) {
	fail := func(message string) (helm.Property, bool) {
		c.report(DiagnosticError, path, "$ref", message)
		return helm.Property{}, false
	}

	if !strings.HasPrefix(reference, "#") {
		return fail(fmt.Sprintf("reference %q is not a local JSON pointer and cannot be resolved", reference))
	}

	pointer := strings.TrimPrefix(reference, "#")
	pointer = strings.TrimPrefix(pointer, "/")
	if len(pointer) == 0 {
		return fail("root reference \"#\" cannot be resolved")
	}

	tokens := strings.Split(pointer, "/")
	for index := range tokens {
		tokens[index] = unescapeJSONPointerToken(tokens[index])
	}

	var current helm.Property
	var ok bool

	switch tokens[0] {
	case "$defs":
		if len(tokens) < 2 {
			return fail(fmt.Sprintf("reference %q does not name a definition", reference))
		}
		current, ok = c.defs[tokens[1]]
		if !ok {
			current, ok = c.legacyDefs[tokens[1]]
		}
		tokens = tokens[2:]
	case "definitions":
		if len(tokens) < 2 {
			return fail(fmt.Sprintf("reference %q does not name a definition", reference))
		}
		current, ok = c.legacyDefs[tokens[1]]
		if !ok {
			current, ok = c.defs[tokens[1]]
		}
		tokens = tokens[2:]
	case "properties":
		// The root schema is not carried here, but a root-relative property
		// pointer is equivalent to the same definition path in practice. Resolve
		// the property name as a definition; report clearly when that fails.
		if len(tokens) < 2 {
			return fail(fmt.Sprintf("reference %q does not name a property", reference))
		}
		current, ok = c.defs[tokens[1]]
		if !ok {
			current, ok = c.legacyDefs[tokens[1]]
		}
		if !ok {
			return fail(fmt.Sprintf("reference %q cannot be resolved: root properties are not available", reference))
		}
		tokens = tokens[2:]
	default:
		// Be lenient with a bare definition name; this keeps older schemas that
		// used "#/name" working.
		current, ok = c.defs[tokens[0]]
		if !ok {
			current, ok = c.legacyDefs[tokens[0]]
		}
		tokens = tokens[1:]
	}

	if !ok {
		return fail(fmt.Sprintf("reference %q cannot be resolved", reference))
	}

	for index := 0; index < len(tokens); index++ {
		token := tokens[index]

		switch token {
		case "$defs", "definitions", "properties":
			if index+1 >= len(tokens) {
				return fail(fmt.Sprintf("reference %q ends on the %q keyword", reference, token))
			}
			index++
			current, ok = lookupKeyword(current, token, tokens[index])
		default:
			current, ok = lookupToken(current, token)
		}

		if !ok {
			return fail(fmt.Sprintf("reference %q cannot be resolved beyond %q", reference, token))
		}
	}

	return current, true
}

func lookupKeyword(node helm.Property, keyword, name string) (helm.Property, bool) {
	switch keyword {
	case "$defs":
		property, ok := node.Definitions[name]
		return property, ok
	case "definitions":
		property, ok := node.LegacyDefinitions[name]
		return property, ok
	case "properties":
		property, ok := node.Properties[name]
		return property, ok
	default:
		return helm.Property{}, false
	}
}

// lookupToken resolves one path segment inside a definition, preferring nested
// definitions over properties so a definitions path is followed as such.
func lookupToken(node helm.Property, token string) (helm.Property, bool) {
	if property, ok := node.Definitions[token]; ok {
		return property, true
	}

	if property, ok := node.LegacyDefinitions[token]; ok {
		return property, true
	}

	if property, ok := node.Properties[token]; ok {
		return property, true
	}

	return helm.Property{}, false
}

func unescapeJSONPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~1", "/")
	token = strings.ReplaceAll(token, "~0", "~")
	return token
}

// mergeAllOf flattens allOf by merging every branch into a single schema:
// properties and required are unioned, and a same-named property declared with
// conflicting types is reported with its path instead of being silently
// overwritten.
func (c *converter) mergeAllOf(schema helm.Property, path string) helm.Property {
	return c.mergeAllOfDepth(schema, path, 0)
}

func (c *converter) mergeAllOfDepth(schema helm.Property, path string, depth int) helm.Property {
	if depth > maxConversionDepth {
		c.report(DiagnosticWarning, path, "allOf", fmt.Sprintf("maximum conversion depth of %d exceeded; allOf merge truncated", maxConversionDepth))
		merged := schema
		merged.AllOf = nil
		return merged
	}

	merged := schema
	merged.AllOf = nil
	merged.Properties = cloneProperties(schema.Properties)
	merged.Required = append([]string(nil), schema.Required...)

	for index, branch := range schema.AllOf {
		branchPath := fmt.Sprintf("%s.allOf[%d]", path, index)

		if branch.HasRef() {
			resolved, ok := c.resolveRef(branch.Reference, branchPath)
			if !ok {
				continue
			}
			branch = resolved
		}

		if len(branch.AllOf) > 0 {
			branch = c.mergeAllOfDepth(branch, branchPath, depth+1)
		}

		merged = c.mergePropertyInto(merged, branch, path)
	}

	return merged
}

func cloneProperties(properties map[string]helm.Property) map[string]helm.Property {
	if properties == nil {
		return nil
	}

	cloned := make(map[string]helm.Property, len(properties))
	for name, property := range properties {
		cloned[name] = property
	}

	return cloned
}

func (c *converter) mergePropertyInto(target, branch helm.Property, path string) helm.Property {
	if len(branch.Type) > 0 {
		if len(target.Type) > 0 && target.Type != branch.Type {
			c.report(DiagnosticError, path, "allOf", fmt.Sprintf("conflicting types %q and %q for the same property", target.Type, branch.Type))
		} else if len(target.Type) == 0 {
			target.Type = branch.Type
		}
	}

	if len(target.Title) == 0 {
		target.Title = branch.Title
	}

	if len(target.Description) == 0 {
		target.Description = branch.Description
	}

	if len(target.Enum) == 0 {
		target.Enum = branch.Enum
	}

	if target.Const == nil {
		target.Const = branch.Const
	}

	if len(branch.Properties) > 0 {
		merged := cloneProperties(target.Properties)
		if merged == nil {
			merged = make(map[string]helm.Property, len(branch.Properties))
		}

		for propertyName, property := range branch.Properties {
			existing, ok := merged[propertyName]
			if !ok {
				merged[propertyName] = property
				continue
			}

			merged[propertyName] = c.mergePropertyInto(existing, property, path+"."+propertyName)
		}

		target.Properties = merged
	}

	target.Required = unionStrings(target.Required, branch.Required)

	return target
}

func unionStrings(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	result := make([]string, 0, len(base)+len(extra))

	for _, value := range base {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	for _, value := range extra {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

// compositionField turns oneOf/anyOf into a field with selectable variants. The
// historical "prefer a boolean branch" rule is kept only for the boolean+object
// combination it was meant for; every other combination becomes variants so no
// branch (in particular an object branch) is lost.
func (c *converter) compositionField(name, path string, schema helm.Property, depth int, dependencies []*models.Template) models.Field {
	branches := schema.OneOf
	keyword := "oneOf"
	if len(branches) == 0 {
		branches = schema.AnyOf
		keyword = "anyOf"
	} else if len(schema.AnyOf) > 0 {
		c.report(DiagnosticWarning, path, "anyOf", "both oneOf and anyOf are present; only oneOf is rendered")
	}

	resolved := make([]helm.Property, 0, len(branches))
	for index, branch := range branches {
		branchPath := fmt.Sprintf("%s.%s[%d]", path, keyword, index)

		if branch.HasRef() {
			property, ok := c.resolveRef(branch.Reference, branchPath)
			if !ok {
				continue
			}
			branch = property
		}

		// Properties declared next to oneOf/anyOf apply to every branch; fold
		// them into each branch so they are not silently dropped.
		branch = c.mergeSiblingProperties(schema, branch, branchPath)

		resolved = append(resolved, branch)
	}

	nonNull := make([]helm.Property, 0, len(resolved))
	for _, branch := range resolved {
		if isNullOnly(branch) {
			continue
		}
		nonNull = append(nonNull, branch)
	}

	if len(nonNull) != len(resolved) {
		c.report(DiagnosticWarning, path+"."+keyword, "null", "null-only branches are not rendered")
	}

	if len(nonNull) == 0 {
		field := c.baseField(name, schema, path)
		if len(field.Type) == 0 {
			field.Type = "string"
		}
		c.report(DiagnosticWarning, path+"."+keyword, keyword, "every branch only allows null; field rendered as a string")
		return field
	}

	if len(nonNull) == 1 {
		field := c.convert(name, path, nonNull[0], depth+1, dependencies)
		if len(schema.Title) > 0 {
			field.DisplayName = mapTitle(name, schema.Title)
		}
		if len(field.Description) == 0 {
			field.Description = schema.Description
		}
		return field
	}

	if boolean, ok := preferBooleanBranch(nonNull); ok {
		return c.convert(name, path, boolean, depth+1, dependencies)
	}

	discriminator, titles := detectDiscriminator(nonNull)

	variants := make([]models.FieldVariant, 0, len(nonNull))
	for index, branch := range nonNull {
		title := branch.Title
		if len(title) == 0 && index < len(titles) {
			title = titles[index]
		}
		if len(title) == 0 {
			title = fmt.Sprintf("Variant %d", index+1)
		}

		subtree := c.convert(name, fmt.Sprintf("%s.%s[%d]", path, keyword, index), branch, depth+1, nil)
		subtree.DisplayName = title

		variants = append(variants, models.FieldVariant{
			Title:  title,
			Fields: subtree,
		})
	}

	field := c.baseField(name, schema, path)
	field.Type = variantType(nonNull)
	field.Variants = variants
	field.Discriminator = discriminator
	field.HasDiscriminator = len(discriminator) > 0
	if !field.HasDiscriminator {
		field.VariantNote = "no discriminator"
	}

	return field
}

// mergeSiblingProperties folds properties/required declared alongside
// oneOf/anyOf into each branch, since JSON Schema applies them to every match.
func (c *converter) mergeSiblingProperties(parent, branch helm.Property, path string) helm.Property {
	if len(parent.Properties) == 0 && len(parent.Required) == 0 {
		return branch
	}

	if len(branch.Properties) == 0 && len(branch.Type) == 0 {
		branch.Type = "object"
	}

	for _, propertyName := range sortedKeys(parent.Properties) {
		property := parent.Properties[propertyName]
		existing, ok := branch.Properties[propertyName]
		if !ok {
			if branch.Properties == nil {
				branch.Properties = make(map[string]helm.Property, len(parent.Properties))
			}
			branch.Properties[propertyName] = property
			continue
		}

		branch.Properties[propertyName] = c.mergePropertyInto(existing, property, path+"."+propertyName)
	}

	branch.Required = unionStrings(branch.Required, parent.Required)
	return branch
}

func isNullOnly(property helm.Property) bool {
	return property.Type == "null" &&
		len(property.Properties) == 0 &&
		property.Items == nil &&
		len(property.OneOf) == 0 &&
		len(property.AnyOf) == 0 &&
		len(property.AllOf) == 0 &&
		property.Const == nil
}

// preferBooleanBranch keeps the legacy rule for the exact combination it
// targets: a boolean branch alongside object branches. Applying it to any other
// combination would drop a non-object branch.
func preferBooleanBranch(branches []helm.Property) (helm.Property, bool) {
	var boolean *helm.Property
	hasObject := false

	for index := range branches {
		branch := &branches[index]

		switch mapHelmPropertyTypeToFieldType(*branch) {
		case "boolean":
			if boolean == nil {
				boolean = branch
			}
		case "object":
			hasObject = true
		default:
			return helm.Property{}, false
		}
	}

	if boolean == nil || !hasObject {
		return helm.Property{}, false
	}

	return *boolean, true
}

func variantType(branches []helm.Property) string {
	if len(branches) == 0 {
		return "object"
	}

	first := mapHelmPropertyTypeToFieldType(branches[0])
	if len(first) == 0 {
		return "object"
	}

	for _, branch := range branches[1:] {
		if mapHelmPropertyTypeToFieldType(branch) != first {
			return "object"
		}
	}

	return first
}

// detectDiscriminator finds a property that every branch constrains to a
// constant (const or a single-valued enum) with distinct values. Candidate
// names are visited in sorted order so the choice is deterministic.
func detectDiscriminator(branches []helm.Property) (string, []string) {
	if len(branches) == 0 {
		return "", nil
	}

	candidates := make([]string, 0, len(branches[0].Properties))
	for name := range branches[0].Properties {
		candidates = append(candidates, name)
	}
	sort.Strings(candidates)

	for _, candidate := range candidates {
		values := make([]interface{}, len(branches))
		distinct := make(map[string]struct{}, len(branches))
		usable := true

		for index, branch := range branches {
			value, ok := constantValue(branch, candidate)
			if !ok {
				usable = false
				break
			}

			values[index] = value

			key := fmt.Sprintf("%T:%v", value, value)
			if _, seen := distinct[key]; seen {
				usable = false
				break
			}
			distinct[key] = struct{}{}
		}

		if !usable {
			continue
		}

		titles := make([]string, len(values))
		for index, value := range values {
			titles[index] = fmt.Sprintf("%v", value)
		}

		return candidate, titles
	}

	return "", nil
}

func constantValue(branch helm.Property, propertyName string) (interface{}, bool) {
	property, ok := branch.Properties[propertyName]
	if !ok {
		return nil, false
	}

	if property.Const != nil {
		if value, ok := property.Const.ScalarValue(); ok {
			return value, true
		}
	}

	if len(property.Enum) == 1 {
		return property.Enum[0], true
	}

	return nil, false
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortFields(fields []models.Field, order []string) []models.Field {
	// Create a map to store the custom order indices
	ordersMap := make(map[string]int)
	for i, name := range order {
		ordersMap[name] = i
	}

	// Separate fields with order and without order
	var orderedFields []models.Field
	var unorderedFields []models.Field

	for _, field := range fields {
		if _, ok := ordersMap[field.Name]; ok {
			orderedFields = append(orderedFields, field)
		} else {
			unorderedFields = append(unorderedFields, field)
		}
	}

	// Sort fields with order based on the order index
	sort.Slice(orderedFields, func(i, j int) bool {
		return ordersMap[orderedFields[i].Name] < ordersMap[orderedFields[j].Name]
	})

	// Sort fields without order alphabetically by name
	sort.Slice(unorderedFields, func(i, j int) bool {
		return unorderedFields[i].Name < unorderedFields[j].Name
	})

	// Combine the ordered and unordered fields
	sortedFields := append(orderedFields, unorderedFields...)

	return sortedFields
}

func mapHelmPropertyTypeToFieldType(property helm.Property) string {
	switch property.Type {
	case "string":
		return "string"
	case "integer":
		return "number"
	case "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		return "array"
	case "object":
		if len(property.Properties) == 0 {
			return "map"
		}

		return "object"
	default:
		if len(property.Properties) > 0 {
			return "object"
		}

		if property.Items != nil {
			return "array"
		}

		return string(property.Type)
	}
}

func mapTitle(name, displayName string) string {
	if len(displayName) != 0 {
		return displayName
	}

	return name
}
