package tools

import (
	"encoding/json"
	"fmt"
)

// Kind is the type of one argument value.
//
// The set is deliberately small: it covers what a tool can actually ask a model
// for, and every member has one unambiguous JSON Schema spelling. Growing it is
// a decision about what tools may accept, which is why there is no escape hatch
// for an arbitrary schema fragment — a fragment nobody validated reaches the
// model as whatever it happens to be.
type Kind string

const (
	KindString  Kind = "string"
	KindNumber  Kind = "number"
	KindBoolean Kind = "boolean"
	KindArray   Kind = "array"
	KindObject  Kind = "object"
)

// Parameter is one argument a tool accepts.
type Parameter struct {
	// Name is the key the model puts this value under.
	Name string

	// Kind is the value's type.
	Kind Kind

	// Description is what the model reads to decide what to pass. It is not
	// decoration: an argument whose meaning is only obvious from its name is
	// one the model guesses at.
	Description string

	// Required says the call is malformed without this argument.
	//
	// Carried on the parameter rather than in a separate list of names,
	// because a list is a second place to edit — a renamed parameter that stays
	// required somewhere else produces a schema demanding an argument no tool
	// accepts.
	Required bool

	// Elements describes what an array holds. Required when Kind is KindArray,
	// and meaningless on anything else.
	Elements *Value
}

// Value is a type with no name of its own — what an array holds.
type Value struct {
	Kind Kind

	// Fields are an object's members, and are required when Kind is KindObject.
	Fields []Parameter
}

// Schema describes the argument object a tool is called with.
//
// A tool that takes no arguments has no Schema rather than an empty one: the
// two say different things to a model, and an empty object still tells it there
// is an argument shape to fill in.
type Schema struct {
	Parameters []Parameter
}

// Validate reports a schema that could not be honoured.
//
// Checked when a tool is registered rather than when the model calls it: a
// malformed schema is a mistake in the program, and finding it at startup costs
// nothing, while finding it on a call means a model has already been told to
// use something that cannot work.
func (s *Schema) Validate() error {
	if s == nil {
		return nil
	}
	if len(s.Parameters) == 0 {
		return fmt.Errorf("tools: a schema with no parameters declares an argument object with nothing in it; use no schema instead")
	}
	return validateParameters(s.Parameters)
}

func validateParameters(params []Parameter) error {
	seen := make(map[string]bool, len(params))
	for _, p := range params {
		if p.Name == "" {
			return fmt.Errorf("tools: a parameter has no name")
		}
		if seen[p.Name] {
			return fmt.Errorf("tools: parameter %q is declared twice", p.Name)
		}
		seen[p.Name] = true
		if err := validateKind(p.Name, p.Kind, p.Elements); err != nil {
			return err
		}
	}
	return nil
}

func validateKind(name string, kind Kind, elements *Value) error {
	switch kind {
	case KindString, KindNumber, KindBoolean:
		if elements != nil {
			return fmt.Errorf("tools: parameter %q is a %s and cannot describe elements", name, kind)
		}
		return nil
	case KindArray:
		if elements == nil {
			return fmt.Errorf("tools: array parameter %q does not say what it holds", name)
		}
		switch elements.Kind {
		case KindString, KindNumber, KindBoolean:
			if len(elements.Fields) > 0 {
				return fmt.Errorf("tools: parameter %q holds %ss, which have no fields", name, elements.Kind)
			}
			return nil
		case KindObject:
			if len(elements.Fields) == 0 {
				return fmt.Errorf("tools: parameter %q holds objects with no fields", name)
			}
			return validateParameters(elements.Fields)
		default:
			return fmt.Errorf("tools: parameter %q holds an unknown kind %q", name, elements.Kind)
		}
	case KindObject:
		return fmt.Errorf("tools: parameter %q is a bare object; declare its fields as parameters instead", name)
	case "":
		return fmt.Errorf("tools: parameter %q has no kind", name)
	default:
		return fmt.Errorf("tools: parameter %q has an unknown kind %q", name, kind)
	}
}

// JSON renders the schema as the JSON Schema document a model is given.
//
// This is the model-facing contract, so it is built here rather than wherever
// the framework adapter happens to live: the same document is what a test can
// read, and a schema nobody can inspect without starting a runtime is a schema
// nobody checks.
func (s *Schema) JSON() ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(objectDoc(s.Parameters))
}

// jsonDoc keeps the emitted keys ordered as JSON Schema readers expect them.
// A map would serialise in whatever order Go chose, which makes two identical
// schemas compare unequal as bytes for no reason a reader can see.
type jsonDoc struct {
	Type        string             `json:"type"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]jsonDoc `json:"properties,omitempty"`
	Items       *jsonDoc           `json:"items,omitempty"`
	Required    []string           `json:"required,omitempty"`
}

func objectDoc(params []Parameter) jsonDoc {
	doc := jsonDoc{Type: string(KindObject), Properties: map[string]jsonDoc{}}
	// Required is built in declaration order rather than by ranging the
	// properties map, so the document is the same bytes every time it is built.
	for _, p := range params {
		doc.Properties[p.Name] = valueDoc(p)
		if p.Required {
			doc.Required = append(doc.Required, p.Name)
		}
	}
	return doc
}

func valueDoc(p Parameter) jsonDoc {
	doc := jsonDoc{Type: string(p.Kind), Description: p.Description}
	if p.Kind != KindArray {
		return doc
	}
	var items jsonDoc
	if p.Elements.Kind == KindObject {
		items = objectDoc(p.Elements.Fields)
	} else {
		items = jsonDoc{Type: string(p.Elements.Kind)}
	}
	doc.Items = &items
	return doc
}
