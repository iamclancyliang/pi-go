package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// declared is a tool that exists to carry a schema into a Registry.
type declared struct {
	name   string
	params *tools.Schema
}

func (d *declared) Name() string               { return d.name }
func (d *declared) Description() string        { return "a tool under test" }
func (d *declared) Execution() tools.Execution { return tools.Execution{ReadOnly: true} }
func (d *declared) Parameters() *tools.Schema  { return d.params }
func (d *declared) Call(context.Context, string) (tools.Result, error) {
	return tools.Result{}, nil
}

// TestASchemaRendersWhatAModelIsGiven pins the document itself, field by field,
// rather than that rendering merely succeeded: the model reads this, and a
// property that silently loses its description or its type is a tool the model
// has to guess at.
func TestASchemaRendersWhatAModelIsGiven(t *testing.T) {
	s := &tools.Schema{Parameters: []tools.Parameter{
		{Name: "path", Kind: tools.KindString, Description: "Path to edit", Required: true},
		{Name: "edits", Kind: tools.KindArray, Description: "Replacements", Required: true,
			Elements: &tools.Value{Kind: tools.KindObject, Fields: []tools.Parameter{
				{Name: "oldText", Kind: tools.KindString, Description: "Exact text", Required: true},
				{Name: "newText", Kind: tools.KindString, Description: "Replacement", Required: true},
			}}},
		{Name: "limit", Kind: tools.KindNumber, Description: "Maximum results"},
	}}

	raw, err := s.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var doc struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
			Items       *struct {
				Type       string   `json:"type"`
				Required   []string `json:"required"`
				Properties map[string]struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the rendered schema is not JSON: %v", err)
	}

	if doc.Type != "object" {
		t.Fatalf("arguments render as %q, not an object", doc.Type)
	}
	if got := doc.Properties["path"]; got.Type != "string" || got.Description != "Path to edit" {
		t.Fatalf("path rendered as %+v", got)
	}
	if got := doc.Properties["limit"]; got.Type != "number" || got.Description != "Maximum results" {
		t.Fatalf("limit rendered as %+v", got)
	}

	// Only what was declared required may be required: a model told an optional
	// argument is mandatory stops making calls it was allowed to make.
	if len(doc.Required) != 2 {
		t.Fatalf("required is %v, want exactly path and edits", doc.Required)
	}
	required := map[string]bool{}
	for _, name := range doc.Required {
		required[name] = true
	}
	if !required["path"] || !required["edits"] {
		t.Fatalf("required is %v, want path and edits", doc.Required)
	}

	edits := doc.Properties["edits"]
	if edits.Type != "array" || edits.Items == nil {
		t.Fatalf("edits rendered as %+v, and a model cannot fill an array that never says what it holds", edits)
	}
	if edits.Items.Type != "object" {
		t.Fatalf("edits holds %q, not objects", edits.Items.Type)
	}
	if got := edits.Items.Properties["oldText"]; got.Description != "Exact text" {
		t.Fatalf("the nested oldText rendered as %+v", got)
	}
	if len(edits.Items.Required) != 2 {
		t.Fatalf("the nested required is %v, want both fields", edits.Items.Required)
	}
}

// TestTheSameSchemaRendersTheSameBytes guards the ordering that a map would
// decide at random. Two builds of one schema that differ as bytes make every
// comparison against a recorded document fail for a reason nobody can see.
func TestTheSameSchemaRendersTheSameBytes(t *testing.T) {
	s := &tools.Schema{Parameters: []tools.Parameter{
		{Name: "a", Kind: tools.KindString, Required: true},
		{Name: "b", Kind: tools.KindString, Required: true},
		{Name: "c", Kind: tools.KindString, Required: true},
	}}
	first, err := s.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := s.JSON()
		if err != nil {
			t.Fatalf("JSON: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("one schema rendered two documents:\n%s\n%s", first, again)
		}
	}
}

// TestAToolWithNoArgumentsCarriesNoSchema keeps "takes nothing" distinct from
// "takes an object with nothing in it". The second still tells a model there is
// a shape to fill in.
func TestAToolWithNoArgumentsCarriesNoSchema(t *testing.T) {
	var none *tools.Schema
	raw, err := none.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if raw != nil {
		t.Fatalf("a tool taking no arguments rendered %s", raw)
	}
	if err := none.Validate(); err != nil {
		t.Fatalf("a tool taking no arguments was rejected: %v", err)
	}
}

// TestRegisteringRefusesASchemaAModelCouldNotUse pins that the check happens at
// registration. Past that moment the schema is something a model was told it
// could use, and the mistake is no longer only the program's.
func TestRegisteringRefusesASchemaAModelCouldNotUse(t *testing.T) {
	for name, params := range map[string]*tools.Schema{
		"an array that never says what it holds": {Parameters: []tools.Parameter{
			{Name: "edits", Kind: tools.KindArray, Required: true},
		}},
		"an object element with no fields": {Parameters: []tools.Parameter{
			{Name: "edits", Kind: tools.KindArray, Required: true,
				Elements: &tools.Value{Kind: tools.KindObject}},
		}},
		"a parameter with no kind": {Parameters: []tools.Parameter{
			{Name: "path", Required: true},
		}},
		"a parameter with no name": {Parameters: []tools.Parameter{
			{Kind: tools.KindString},
		}},
		"one name declared twice": {Parameters: []tools.Parameter{
			{Name: "path", Kind: tools.KindString},
			{Name: "path", Kind: tools.KindNumber},
		}},
		"a bare object": {Parameters: []tools.Parameter{
			{Name: "opts", Kind: tools.KindObject},
		}},
		"an argument object with nothing in it": {},
	} {
		t.Run(name, func(t *testing.T) {
			r := tools.NewRegistry()
			if err := r.Register(&declared{name: "t", params: params}); err == nil {
				t.Fatalf("%s was accepted, and the model would have been offered it", name)
			}
			if _, found := r.Lookup("t"); found {
				t.Fatal("a tool rejected for its schema was registered anyway")
			}
		})
	}
}

// TestAUsableSchemaRegisters is the other half: the guard must not refuse the
// shapes the built-in tools actually need.
func TestAUsableSchemaRegisters(t *testing.T) {
	r := tools.NewRegistry()
	err := r.Register(&declared{name: "edit", params: &tools.Schema{Parameters: []tools.Parameter{
		{Name: "path", Kind: tools.KindString, Required: true},
		{Name: "edits", Kind: tools.KindArray, Required: true,
			Elements: &tools.Value{Kind: tools.KindObject, Fields: []tools.Parameter{
				{Name: "oldText", Kind: tools.KindString, Required: true},
			}}},
		{Name: "ignoreCase", Kind: tools.KindBoolean},
		{Name: "globs", Kind: tools.KindArray, Elements: &tools.Value{Kind: tools.KindString}},
	}}})
	if err != nil {
		t.Fatalf("a schema the built-in tools need was refused: %v", err)
	}
	if _, found := r.Lookup("edit"); !found {
		t.Fatal("the tool did not register")
	}
}
