package runtime

import (
	"context"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// schemaTool carries a declared schema across the framework boundary.
type schemaTool struct {
	params *tools.Schema
}

func (s *schemaTool) Name() string               { return "edit" }
func (s *schemaTool) Description() string        { return "edit a file" }
func (s *schemaTool) Execution() tools.Execution { return tools.Execution{} }
func (s *schemaTool) Parameters() *tools.Schema  { return s.params }
func (s *schemaTool) Call(context.Context, string) (tools.Result, error) {
	return tools.Result{}, nil
}

// TestAToolsSchemaReachesTheModel pins the only path a declared argument shape
// has to the provider.
//
// Info is where a pi-go tool becomes something the framework will describe to a
// model. A schema that stops here is not a visible failure: the tool is still
// offered, the model still calls it, and the call arrives with arguments the
// model invented — which surfaces as a malformed payload rather than as a
// missing declaration.
func TestAToolsSchemaReachesTheModel(t *testing.T) {
	adapted := &observedTool{inner: &schemaTool{params: &tools.Schema{
		Parameters: []tools.Parameter{
			{Name: "path", Kind: tools.KindString, Description: "Path to edit", Required: true},
		},
	}}}

	info, err := adapted.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "edit" || info.Desc != "edit a file" {
		t.Fatalf("the tool arrived as %q/%q", info.Name, info.Desc)
	}
	if info.ParamsOneOf == nil {
		t.Fatal("a declared argument shape did not cross the boundary, and the model would invent one")
	}

	// Read back through the framework's own conversion rather than the struct
	// we handed it: what the model is told is what that conversion produces,
	// and a schema this accepts but cannot express is still a schema the model
	// never sees.
	converted, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("the framework could not express the schema: %v", err)
	}
	if converted == nil || converted.Properties == nil {
		t.Fatalf("the arguments arrived with no properties: %+v", converted)
	}
	path, found := converted.Properties.Get("path")
	if !found {
		t.Fatal("path did not survive the conversion")
	}
	if path.Description != "Path to edit" {
		t.Fatalf("path arrived described as %q", path.Description)
	}
	if len(converted.Required) != 1 || converted.Required[0] != "path" {
		t.Fatalf("required arrived as %v", converted.Required)
	}
}

// TestAToolWithNoArgumentsIsDescribedAsTakingNone keeps the distinction the
// seam makes: no schema is not an empty one, and an empty object would tell a
// model there is a shape to fill in.
func TestAToolWithNoArgumentsIsDescribedAsTakingNone(t *testing.T) {
	adapted := &observedTool{inner: &schemaTool{params: nil}}
	info, err := adapted.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.ParamsOneOf != nil {
		t.Fatal("a tool taking no arguments was described as taking an object")
	}
}

// TestAnUnusableSchemaFailsBeforeTheModelIsTold covers the tool that reaches
// this boundary without having gone through Register — an extension host, or a
// registry built by hand in a test. The framework must not be handed something
// that cannot be expressed.
func TestAnUnusableSchemaFailsBeforeTheModelIsTold(t *testing.T) {
	adapted := &observedTool{inner: &schemaTool{params: &tools.Schema{
		Parameters: []tools.Parameter{{Name: "edits", Kind: tools.KindArray, Required: true}},
	}}}
	if _, err := adapted.Info(context.Background()); err == nil {
		t.Fatal("an array that never says what it holds was described to the model")
	}
}
