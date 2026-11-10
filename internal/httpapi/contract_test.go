package httpapi

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/alvaroibarguen/dbairlock/internal/settings"
)

// secretExtension marks a schema property as a secret in api/openapi.yaml.
const secretExtension = "x-dbairlock-secret"

func loadContract(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func isSecretSchema(s *openapi3.Schema) bool {
	if s == nil {
		return false
	}
	v, ok := s.Extensions[secretExtension]
	return s.WriteOnly || (ok && fmt.Sprint(v) == "true")
}

// walkSchema calls fn for every property schema reachable from ref, with a
// dotted location.
func walkSchema(ref *openapi3.SchemaRef, loc string, seen map[*openapi3.Schema]bool, fn func(loc string, s *openapi3.Schema)) {
	if ref == nil || ref.Value == nil || seen[ref.Value] {
		return
	}
	s := ref.Value
	seen[s] = true
	defer delete(seen, s) // a shared schema is checked on every path that reaches it
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range s.Properties {
			if !yield(k) {
				return
			}
		}
	}) {
		p := s.Properties[name]
		if p != nil && p.Value != nil {
			fn(loc+"."+name, p.Value)
		}
		walkSchema(p, loc+"."+name, seen, fn)
	}
	walkSchema(s.Items, loc+"[]", seen, fn)
	walkSchema(s.AdditionalProperties.Schema, loc+"{}", seen, fn)
	walkSchema(s.Not, loc+"!", seen, fn)
	for _, group := range []openapi3.SchemaRefs{s.AllOf, s.AnyOf, s.OneOf, s.PrefixItems} {
		for i, sub := range group {
			walkSchema(sub, fmt.Sprintf("%s<%d>", loc, i), seen, fn)
		}
	}
}

// secretsInResponses lists every secret property reachable from any
// response schema of any operation.
func secretsInResponses(doc *openapi3.T) []string {
	var found []string
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.Responses == nil {
				continue
			}
			for status, resp := range op.Responses.Map() {
				if resp.Value == nil {
					continue
				}
				for ctype, mt := range resp.Value.Content {
					loc := fmt.Sprintf("%s %s %s %s", method, path, status, ctype)
					if mt.Schema != nil && isSecretSchema(mt.Schema.Value) {
						found = append(found, loc)
					}
					walkSchema(mt.Schema, loc, map[*openapi3.Schema]bool{}, func(l string, s *openapi3.Schema) {
						if isSecretSchema(s) {
							found = append(found, l)
						}
					})
				}
			}
		}
	}
	sort.Strings(found)
	return found
}

func TestNoSecretInAnyResponseSchema(t *testing.T) {
	if found := secretsInResponses(loadContract(t)); len(found) > 0 {
		t.Fatalf("secret properties in response schemas:\n%s", strings.Join(found, "\n"))
	}
}

// TestSecretCheckCatchesALeak proves the check above would fail if a secret
// property were added to a response schema, directly or nested.
func TestSecretCheckCatchesALeak(t *testing.T) {
	str := &openapi3.Types{"string"}
	cases := map[string]func(doc *openapi3.T){
		"writeOnly on the response object": func(doc *openapi3.T) {
			doc.Components.Schemas["Settings"].Value.Properties["client_secret"] =
				&openapi3.SchemaRef{Value: &openapi3.Schema{Type: str, WriteOnly: true}}
		},
		"extension in a nested schema": func(doc *openapi3.T) {
			doc.Components.Schemas["SecretStatus"].Value.Properties["value"] =
				&openapi3.SchemaRef{Value: &openapi3.Schema{Type: str, Extensions: map[string]any{secretExtension: true}}}
		},
		"request schema reused as a response": func(doc *openapi3.T) {
			doc.Components.Schemas["Settings"].Value.Properties["echo"] = doc.Components.Schemas["SettingsSecretsPatch"]
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := loadContract(t)
			mutate(doc)
			if found := secretsInResponses(doc); len(found) == 0 {
				t.Fatal("the leak was not detected")
			}
		})
	}
}

// TestSecretPropertiesAreWriteOnly checks that every property marked as a
// secret is also writeOnly, so generated clients treat it as such.
func TestSecretPropertiesAreWriteOnly(t *testing.T) {
	doc := loadContract(t)
	n := 0
	for name, ref := range doc.Components.Schemas {
		walkSchema(ref, name, map[*openapi3.Schema]bool{}, func(loc string, s *openapi3.Schema) {
			if _, ok := s.Extensions[secretExtension]; ok {
				n++
				if !s.WriteOnly {
					t.Errorf("%s is a secret but not writeOnly", loc)
				}
			}
		})
	}
	if n == 0 {
		t.Fatal("no secret properties found; is the extension name right?")
	}
}

// TestSettingsContractMatchesRegistry keeps api/openapi.yaml and the
// settings registry in step: same keys, types and bounds.
func TestSettingsContractMatchesRegistry(t *testing.T) {
	doc := loadContract(t)
	schemas := doc.Components.Schemas
	defs := settings.Definitions()
	var keys []string
	for _, d := range defs {
		keys = append(keys, d.Key)
	}
	sort.Strings(keys)

	for _, name := range []string{"SettingValues", "SettingValuesPatch"} {
		s := schemas[name].Value
		var props []string
		for k := range s.Properties {
			props = append(props, k)
		}
		sort.Strings(props)
		if !slices.Equal(props, keys) {
			t.Errorf("%s properties %v, registry %v", name, props, keys)
		}
		for _, d := range defs {
			p := s.Properties[d.Key]
			if p == nil {
				continue
			}
			checkSettingSchema(t, name, d, p.Value)
		}
	}
	required := slices.Clone(schemas["SettingValues"].Value.Required)
	sort.Strings(required)
	if !slices.Equal(required, keys) {
		t.Errorf("SettingValues.required = %v", required)
	}
	if len(schemas["SettingValuesPatch"].Value.Required) != 0 {
		t.Error("SettingValuesPatch must not require anything")
	}

	var slots []string
	for _, s := range settings.SecretSlots() {
		slots = append(slots, s.Name)
	}
	sort.Strings(slots)
	for _, name := range []string{"SettingsSecretStatus", "SettingsSecretsPatch"} {
		var props []string
		for k := range schemas[name].Value.Properties {
			props = append(props, k)
		}
		sort.Strings(props)
		if !slices.Equal(props, slots) {
			t.Errorf("%s properties %v, secret slots %v", name, props, slots)
		}
	}
}

func checkSettingSchema(t *testing.T, schema string, d settings.Definition, p *openapi3.Schema) {
	t.Helper()
	loc := schema + "." + d.Key
	want := map[settings.Type]string{
		settings.TypeString: "string", settings.TypeURL: "string", settings.TypeInt: "integer",
		settings.TypeBool: "boolean", settings.TypeStringList: "array",
	}[d.Type]
	if p.Type == nil || !p.Type.Is(want) {
		t.Errorf("%s: type %v, want %s", loc, p.Type, want)
		return
	}
	eqU := func(what string, got *uint64, want int) {
		if (want == 0) != (got == nil) || (got != nil && *got != uint64(want)) { //nolint:gosec // bounds are small and positive
			t.Errorf("%s: %s = %v, want %d", loc, what, got, want)
		}
	}
	switch d.Type {
	case settings.TypeInt:
		if p.Min == nil || p.Max == nil || *p.Min != float64(d.Min) || *p.Max != float64(d.Max) {
			t.Errorf("%s: bounds %v..%v, want %d..%d", loc, p.Min, p.Max, d.Min, d.Max)
		}
	case settings.TypeString, settings.TypeURL:
		eqU("maxLength", p.MaxLength, d.MaxLen)
		if p.MinLength != uint64(d.Min) { //nolint:gosec // bounds are small and positive
			t.Errorf("%s: minLength %d, want %d", loc, p.MinLength, d.Min)
		}
	case settings.TypeStringList:
		eqU("maxItems", p.MaxItems, d.MaxItems)
		if p.Items == nil || p.Items.Value == nil || !p.Items.Value.Type.Is("string") {
			t.Errorf("%s: items must be strings", loc)
		} else {
			eqU("items.maxLength", p.Items.Value.MaxLength, d.MaxLen)
		}
	}
}
