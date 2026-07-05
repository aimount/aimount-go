package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

type lookupOrderInput struct {
	OrderID string `json:"orderId" jsonschema:"required,description=Order id"`
	Verbose bool   `json:"verbose,omitempty" jsonschema:"description=Include verbose order details"`
}

func TestDefineDerivesDefinitionSchema(t *testing.T) {
	definition, err := Define[lookupOrderInput]("lookup_order", "1", "Look up order")
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	if definition.Name != "lookup_order" || definition.Version != "1" || definition.Description != "Look up order" {
		t.Fatalf("unexpected definition: %+v", definition)
	}

	encoded, err := json.Marshal(definition.InputSchema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	text := string(encoded)
	for _, want := range []string{`"type":"object"`, `"orderId"`, `"description":"Order id"`, `"required":["orderId"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("schema missing %s: %s", want, text)
		}
	}
}

func TestDefineRejectsNonStructInput(t *testing.T) {
	if _, err := Define[string]("bad", "1", "Bad"); err == nil {
		t.Fatal("expected non-struct input to fail")
	}
}

func TestOutConstructors(t *testing.T) {
	ok := OK(map[string]any{"ok": true})
	if ok.Error != nil || ok.Result == nil {
		t.Fatalf("unexpected ok out: %+v", ok)
	}

	failed := Err("", "", map[string]any{"field": "orderId"})
	if failed.Result != nil || failed.Error == nil {
		t.Fatalf("unexpected err out: %+v", failed)
	}
	if failed.Error.Code != "tool.internal_error" || failed.Error.Message != "tool execution failed" || failed.Error.Details["field"] != "orderId" {
		t.Fatalf("unexpected error defaults: %+v", failed.Error)
	}
}
