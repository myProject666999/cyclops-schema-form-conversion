package mapper

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cyclops-ui/cyclops/cyclops-ctrl/internal/models"
	"github.com/cyclops-ui/cyclops/cyclops-ctrl/internal/models/helm"
)

type testHelper interface {
	Helper()
	Fatalf(format string, args ...interface{})
}

func mustUnmarshalSchema(t testHelper, raw string) helm.Property {
	t.Helper()

	var schema helm.Property
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatalf("failed to unmarshal schema: %v", err)
	}

	return schema
}

func fieldByName(fields []models.Field, name string) *models.Field {
	for index := range fields {
		if fields[index].Name == name {
			return &fields[index]
		}
	}

	return nil
}

func hasDiagnostic(diagnostics []Diagnostic, keyword string, contains string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Keyword == keyword && strings.Contains(diagnostic.Message, contains) {
			return true
		}
	}

	return false
}

var _ = Describe("Helm schema conversion", func() {
	Describe("exclusive bounds across drafts", func() {
		It("keeps the draft-4 boolean form working", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"replicas": {
						"type": "integer",
						"minimum": 1,
						"exclusiveMinimum": true,
						"maximum": 10,
						"exclusiveMaximum": true
					}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())

			replicas := fieldByName(field.Properties, "replicas")
			Expect(replicas).ToNot(BeNil())
			Expect(replicas.Minimum).ToNot(BeNil())
			Expect(*replicas.Minimum).To(BeNumerically("==", 1))
			Expect(replicas.ExclusiveMinimum).ToNot(BeNil())
			Expect(*replicas.ExclusiveMinimum).To(BeTrue())
			Expect(*replicas.Maximum).To(BeNumerically("==", 10))
			Expect(*replicas.ExclusiveMaximum).To(BeTrue())
		})

		It("accepts the draft-6+ numeric form without failing the whole schema", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"replicas": {
						"type": "integer",
						"exclusiveMinimum": 1,
						"exclusiveMaximum": 10
					},
					"other": {"type": "string"}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())
			// The sibling property proves the schema as a whole deserialised.
			Expect(fieldByName(field.Properties, "other")).ToNot(BeNil())

			replicas := fieldByName(field.Properties, "replicas")
			Expect(replicas.Minimum).ToNot(BeNil())
			Expect(*replicas.Minimum).To(BeNumerically("==", 1))
			Expect(*replicas.ExclusiveMinimum).To(BeTrue())
			Expect(*replicas.Maximum).To(BeNumerically("==", 10))
			Expect(*replicas.ExclusiveMaximum).To(BeTrue())
		})

		It("rejects a non-boolean, non-numeric exclusive bound", func() {
			var schema helm.Property
			err := json.Unmarshal([]byte(`{"type": "number", "exclusiveMinimum": "x"}`), &schema)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("$ref resolution", func() {
		It("resolves #/definitions references", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"image": {"$ref": "#/definitions/imageRef"}
				},
				"definitions": {
					"imageRef": {"type": "string", "description": "container image"}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())

			image := fieldByName(field.Properties, "image")
			Expect(image).ToNot(BeNil())
			Expect(image.Type).To(Equal("string"))
			Expect(image.Description).To(Equal("container image"))
		})

		It("walks nested $defs paths level by level", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"storage": {"$ref": "#/$defs/storage/s3"}
				},
				"$defs": {
					"storage": {
						"$defs": {
							"s3": {
								"type": "object",
								"properties": {"bucket": {"type": "string"}}
							}
						}
					}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())

			storage := fieldByName(field.Properties, "storage")
			Expect(storage).ToNot(BeNil())
			Expect(storage.Type).To(Equal("object"))
			Expect(fieldByName(storage.Properties, "bucket")).ToNot(BeNil())
		})

		It("resolves an array item that is a $ref", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"env": {
						"type": "array",
						"items": {"$ref": "#/$defs/envVar"}
					}
				},
				"$defs": {
					"envVar": {
						"type": "object",
						"required": ["name"],
						"properties": {
							"name": {"type": "string"},
							"value": {"type": "string"}
						}
					}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())

			env := fieldByName(field.Properties, "env")
			Expect(env).ToNot(BeNil())
			Expect(env.Type).To(Equal("array"))
			Expect(env.Items).ToNot(BeNil())
			Expect(env.Items.Type).To(Equal("object"))
			Expect(env.Items.Required).To(ConsistOf("name"))
			Expect(fieldByName(env.Items.Properties, "value")).ToNot(BeNil())
		})

		It("inherits required into array object items", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"servers": {
						"type": "array",
						"items": {
							"type": "object",
							"required": ["host"],
							"properties": {"host": {"type": "string"}}
						}
					}
				}
			}`)

			field, _ := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			servers := fieldByName(field.Properties, "servers")
			Expect(servers.Items.Required).To(ConsistOf("host"))
		})

		It("reports an unresolvable reference with its path", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"broken": {"$ref": "#/$defs/missing"}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)

			// The field is still produced rather than dropped.
			Expect(fieldByName(field.Properties, "broken")).ToNot(BeNil())
			Expect(diagnostics).To(ContainElement(HaveField("Level", DiagnosticError)))
			Expect(hasDiagnostic(diagnostics, "$ref", "cannot be resolved")).To(BeTrue())

			found := false
			for _, diagnostic := range diagnostics {
				if diagnostic.Keyword == "$ref" && diagnostic.Path == "root.broken" {
					found = true
				}
			}
			Expect(found).To(BeTrue())
		})

		It("truncates a self-referencing tree at the depth limit instead of overflowing", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"tree": {"$ref": "#/$defs/node"}
				},
				"$defs": {
					"node": {
						"type": "object",
						"properties": {
							"child": {"$ref": "#/$defs/node"}
						}
					}
				}
			}`)

			Expect(func() {
				field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
				Expect(fieldByName(field.Properties, "tree")).ToNot(BeNil())
				Expect(hasDiagnostic(diagnostics, "$ref", "maximum conversion depth")).To(BeTrue())

				// Depth is bounded: walking child links must not run forever.
				depth := 0
				current := fieldByName(field.Properties, "tree")
				for current != nil {
					next := fieldByName(current.Properties, "child")
					if next == nil {
						break
					}
					depth++
					current = next
					Expect(depth).To(BeNumerically("<=", maxConversionDepth+1))
				}
			}).ToNot(Panic())
		})

		It("truncates a self-referencing array item at the depth limit", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"tree": {"$ref": "#/$defs/node"}
				},
				"$defs": {
					"node": {
						"type": "object",
						"properties": {
							"children": {
								"type": "array",
								"items": {"$ref": "#/$defs/node"}
							}
						}
					}
				}
			}`)

			Expect(func() {
				_, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
				Expect(hasDiagnostic(diagnostics, "$ref", "maximum conversion depth")).To(BeTrue())
			}).ToNot(Panic())
		})
	})

	Describe("oneOf and anyOf", func() {
		It("uses a discriminator constant for oneOf variants", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"storage": {
						"oneOf": [
							{
								"type": "object",
								"required": ["type", "bucket"],
								"properties": {
									"type": {"const": "s3"},
									"bucket": {"type": "string"}
								}
							},
							{
								"type": "object",
								"required": ["type", "path"],
								"properties": {
									"type": {"const": "local"},
									"path": {"type": "string"}
								}
							}
						]
					}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())

			storage := fieldByName(field.Properties, "storage")
			Expect(storage).ToNot(BeNil())
			Expect(storage.HasDiscriminator).To(BeTrue())
			Expect(storage.Discriminator).To(Equal("type"))
			Expect(storage.VariantNote).To(BeEmpty())
			Expect(storage.Variants).To(HaveLen(2))
			Expect(storage.Variants[0].Title).To(Equal("s3"))
			Expect(storage.Variants[1].Title).To(Equal("local"))
			Expect(fieldByName(storage.Variants[0].Fields.Properties, "bucket")).ToNot(BeNil())
			Expect(fieldByName(storage.Variants[1].Fields.Properties, "path")).ToNot(BeNil())
		})

		It("falls back to ordinal variants and marks the absence of a discriminator", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"target": {
						"oneOf": [
							{"type": "object", "properties": {"host": {"type": "string"}}},
							{"type": "object", "properties": {"name": {"type": "string"}}}
						]
					}
				}
			}`)

			field, _ := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			target := fieldByName(field.Properties, "target")
			Expect(target.HasDiscriminator).To(BeFalse())
			Expect(target.Discriminator).To(BeEmpty())
			Expect(target.VariantNote).To(Equal("no discriminator"))
			Expect(target.Variants).To(HaveLen(2))
			Expect(target.Variants[0].Title).To(Equal("Variant 1"))
			Expect(target.Variants[1].Title).To(Equal("Variant 2"))
		})

		It("keeps oneOf and anyOf as separate candidate sets rather than merging them", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"exclusive": {
						"anyOf": [
							{"type": "object", "properties": {"a": {"type": "string"}}},
							{"type": "object", "properties": {"b": {"type": "string"}}}
						]
					},
					"strict": {
						"oneOf": [
							{"type": "object", "properties": {"c": {"type": "string"}}},
							{"type": "object", "properties": {"d": {"type": "string"}}}
						]
					}
				}
			}`)

			field, _ := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(fieldByName(field.Properties, "exclusive").Variants).To(HaveLen(2))
			Expect(fieldByName(field.Properties, "strict").Variants).To(HaveLen(2))
		})

		It("drops a null-only anyOf branch instead of producing a null field", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"optional": {
						"anyOf": [
							{"type": "null"},
							{"type": "object", "properties": {"value": {"type": "string"}}}
						]
					}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			optional := fieldByName(field.Properties, "optional")
			Expect(optional).ToNot(BeNil())
			Expect(optional.Type).To(Equal("object"))
			Expect(fieldByName(optional.Properties, "value")).ToNot(BeNil())
			Expect(hasDiagnostic(diagnostics, "null", "null-only")).To(BeTrue())
		})

		It("keeps the boolean branch only for a boolean+object anyOf", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"feature": {
						"anyOf": [
							{"type": "boolean"},
							{"type": "object", "properties": {"enabled": {"type": "boolean"}}}
						]
					}
				}
			}`)

			field, _ := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			feature := fieldByName(field.Properties, "feature")
			Expect(feature.Type).To(Equal("boolean"))
			Expect(feature.Variants).To(BeEmpty())
		})

		It("preserves the object branch when a boolean sits beside a non-object branch", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"value": {
						"anyOf": [
							{"type": "boolean"},
							{"type": "string"}
						]
					}
				}
			}`)

			field, _ := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			value := fieldByName(field.Properties, "value")
			Expect(value.Variants).To(HaveLen(2))
		})

		It("folds properties declared alongside oneOf into every variant", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"storage": {
						"properties": {
							"name": {"type": "string"}
						},
						"required": ["name"],
						"oneOf": [
							{"type": "object", "properties": {"type": {"const": "s3"}}},
							{"type": "object", "properties": {"type": {"const": "local"}}}
						]
					}
				}
			}`)

			field, _ := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			storage := fieldByName(field.Properties, "storage")
			Expect(storage.Variants).To(HaveLen(2))
			for _, variant := range storage.Variants {
				Expect(fieldByName(variant.Fields.Properties, "name")).ToNot(BeNil())
				Expect(variant.Fields.Required).To(ContainElement("name"))
			}
		})
	})

	Describe("allOf", func() {
		It("merges properties and required from every branch", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"resource": {
						"allOf": [
							{
								"type": "object",
								"required": ["name"],
								"properties": {"name": {"type": "string"}}
							},
							{
								"type": "object",
								"required": ["replicas"],
								"properties": {"replicas": {"type": "integer"}}
							}
						]
					}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())

			resource := fieldByName(field.Properties, "resource")
			Expect(resource).ToNot(BeNil())
			Expect(resource.Type).To(Equal("object"))
			Expect(resource.Required).To(ConsistOf("name", "replicas"))
			Expect(fieldByName(resource.Properties, "name")).ToNot(BeNil())
			replicas := fieldByName(resource.Properties, "replicas")
			Expect(replicas).ToNot(BeNil())
			Expect(replicas.Type).To(Equal("number"))
		})

		It("reports a conflicting property type with its path instead of overwriting", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"resource": {
						"allOf": [
							{"type": "object", "properties": {"replicas": {"type": "string"}}},
							{"type": "object", "properties": {"replicas": {"type": "integer"}}}
						]
					}
				}
			}`)

			_, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(hasDiagnostic(diagnostics, "allOf", "conflicting types")).To(BeTrue())

			found := false
			for _, diagnostic := range diagnostics {
				if diagnostic.Keyword == "allOf" && strings.Contains(diagnostic.Path, "resource.replicas") {
					found = true
				}
			}
			Expect(found).To(BeTrue())
		})
	})

	Describe("conditions", func() {
		It("converts an if/then equality into a display condition", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"mode": {"type": "string"}
				},
				"if": {"properties": {"mode": {"const": "advanced"}}},
				"then": {
					"properties": {"advancedSetting": {"type": "string"}}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())

			advanced := fieldByName(field.Properties, "advancedSetting")
			Expect(advanced).ToNot(BeNil())
			Expect(advanced.Condition).ToNot(BeNil())
			Expect(advanced.Condition.Path).To(Equal("mode"))
			Expect(advanced.Condition.Value).To(Equal("advanced"))
			Expect(advanced.Condition.Present).To(BeFalse())
		})

		It("converts dependentRequired into a conditional-required marker", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"tls": {"type": "boolean"},
					"certificate": {"type": "string"}
				},
				"dependentRequired": {
					"tls": ["certificate"]
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())

			certificate := fieldByName(field.Properties, "certificate")
			Expect(certificate).ToNot(BeNil())
			Expect(certificate.Condition).ToNot(BeNil())
			Expect(certificate.Condition.Path).To(Equal("tls"))
			Expect(certificate.Condition.Present).To(BeTrue())
			Expect(certificate.Condition.Required).To(BeTrue())
		})

		It("converts the draft-7 dependencies array form into a conditional-required marker", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"tls": {"type": "boolean"},
					"certificate": {"type": "string"}
				},
				"dependencies": {
					"tls": ["certificate"]
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(diagnostics).To(BeEmpty())

			certificate := fieldByName(field.Properties, "certificate")
			Expect(certificate.Condition).ToNot(BeNil())
			Expect(certificate.Condition.Path).To(Equal("tls"))
			Expect(certificate.Condition.Required).To(BeTrue())
		})

		It("reports a schema-form dependencies entry as unsupported", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"tls": {"type": "boolean"},
					"certificate": {"type": "string"}
				},
				"dependencies": {
					"tls": {"properties": {"certificate": {"type": "string"}}}
				}
			}`)

			_, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(hasDiagnostic(diagnostics, "dependencies", "not supported")).To(BeTrue())
		})

		It("reports an unsupported conditional a user would otherwise mistake for always-visible", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"mode": {"type": "string"}
				},
				"if": {
					"properties": {
						"mode": {"const": "advanced"},
						"region": {"const": "eu"}
					}
				},
				"then": {
					"properties": {"advancedSetting": {"type": "string"}}
				}
			}`)

			field, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(fieldByName(field.Properties, "advancedSetting")).ToNot(BeNil())
			Expect(hasDiagnostic(diagnostics, "if", "not a single equality")).To(BeTrue())
		})
	})

	Describe("enum and const", func() {
		It("takes the type from type and keeps mixed enum values as-is", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"mixed": {
						"type": "string",
						"enum": ["a", 1, "b"]
					}
				}
			}`)

			field, _ := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			mixed := fieldByName(field.Properties, "mixed")
			Expect(mixed.Type).To(Equal("string"))
			Expect(mixed.Enum).To(Equal([]interface{}{"a", float64(1), "b"}))
		})

		It("keeps a single-valued enum visible but read-only", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["Deployment"]}
				}
			}`)

			field, _ := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			kind := fieldByName(field.Properties, "kind")
			Expect(kind.ReadOnly).To(BeTrue())
			Expect(kind.Enum).To(HaveLen(1))
		})
	})

	Describe("ignored keywords", func() {
		It("reports keywords that cannot be represented instead of dropping them silently", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {
					"labels": {
						"type": "object",
						"patternProperties": {"^[a-z]+$": {"type": "string"}}
					}
				},
				"additionalProperties": {"type": "string"}
			}`)

			_, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(hasDiagnostic(diagnostics, "patternProperties", "ignored")).To(BeTrue())
			Expect(hasDiagnostic(diagnostics, "additionalProperties", "ignored")).To(BeTrue())
		})

		It("does not report the boolean form of additionalProperties", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"additionalProperties": false
			}`)

			_, diagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			Expect(hasDiagnostic(diagnostics, "additionalProperties", "ignored")).To(BeFalse())
		})
	})

	Describe("determinism", func() {
		It("produces identical JSON for repeated conversions of the same schema", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"order": ["b", "a"],
				"properties": {
					"z": {"type": "string"},
					"a": {"type": "string"},
					"b": {
						"oneOf": [
							{"type": "object", "properties": {"y": {"type": "string"}, "x": {"type": "string"}}},
							{"type": "object", "properties": {"w": {"type": "string"}}}
						]
					}
				},
				"dependentRequired": {"z": ["a"]},
				"patternProperties": {"^x": {"type": "string"}}
			}`)

			first, firstDiagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
			firstJSON, err := json.Marshal(first)
			Expect(err).ToNot(HaveOccurred())
			firstDiagnosticsJSON, err := json.Marshal(firstDiagnostics)
			Expect(err).ToNot(HaveOccurred())

			for iteration := 0; iteration < 20; iteration++ {
				next, nextDiagnostics := HelmSchemaToFieldsWithDiagnostics("root", schema, schema.Definitions, nil)
				nextJSON, err := json.Marshal(next)
				Expect(err).ToNot(HaveOccurred())
				nextDiagnosticsJSON, err := json.Marshal(nextDiagnostics)
				Expect(err).ToNot(HaveOccurred())

				Expect(string(nextJSON)).To(Equal(string(firstJSON)))
				Expect(string(nextDiagnosticsJSON)).To(Equal(string(firstDiagnosticsJSON)))
			}
		})
	})

	Describe("backwards compatibility", func() {
		It("keeps the legacy function signature working", func() {
			schema := mustUnmarshalSchema(GinkgoT(), `{
				"type": "object",
				"properties": {"a": {"type": "string"}}
			}`)

			field := HelmSchemaToFields("root", schema, schema.Definitions, nil)
			Expect(fieldByName(field.Properties, "a")).ToNot(BeNil())
		})
	})
})
