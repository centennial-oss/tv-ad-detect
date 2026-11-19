// SPDX-License-Identifier: Apache-2.0

// Command schemagen writes the JSON Schema of the canonical result document
// (OUT-01) by reflecting over addetect.Result, so the published schema can
// never drift from the Go types. Run: go run ./internal/schemagen > doc/result.schema.json
package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
)

type schema map[string]any

var defs = map[string]schema{}

func typeSchema(t reflect.Type) schema {
	switch t.Kind() {
	case reflect.Pointer:
		s := typeSchema(t.Elem())
		return schema{"anyOf": []any{s, schema{"type": "null"}}}
	case reflect.Bool:
		return schema{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return schema{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return schema{"type": "number"}
	case reflect.String:
		return schema{"type": "string"}
	case reflect.Slice:
		return schema{"type": "array", "items": typeSchema(t.Elem())}
	case reflect.Map:
		return schema{"type": "object", "additionalProperties": typeSchema(t.Elem())}
	case reflect.Interface:
		return schema{}
	case reflect.Struct:
		name := t.Name()
		if _, ok := defs[name]; !ok {
			defs[name] = schema{} // reserve (recursion guard)
			props := schema{}
			var required []string
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				tag := f.Tag.Get("json")
				if !f.IsExported() || tag == "-" {
					continue
				}
				key, opts, _ := strings.Cut(tag, ",")
				if key == "" {
					key = f.Name
				}
				props[key] = typeSchema(f.Type)
				if !strings.Contains(opts, "omitempty") {
					required = append(required, key)
				}
			}
			s := schema{"type": "object", "properties": props, "additionalProperties": false}
			if len(required) > 0 {
				s["required"] = required
			}
			defs[name] = s
		}
		return schema{"$ref": "#/$defs/" + name}
	}
	return schema{}
}

func main() {
	root := typeSchema(reflect.TypeOf(addetect.Result{}))
	out := schema{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         "https://github.com/centennial-oss/tv-ad-detect/doc/result.schema.json",
		"title":       "tv-ad-detect analysis result, schema version " + addetect.SchemaVersion,
		"description": "All times are integer microseconds relative to the first presented video frame; intervals are half-open [start_us, end_us). Null marks an unobserved or unavailable measurement.",
		"$ref":        root["$ref"],
		"$defs":       defs,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		os.Exit(1)
	}
}
