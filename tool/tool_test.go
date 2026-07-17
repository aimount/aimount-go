package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type lookupOrderInput struct {
	OrderID string `json:"orderId" jsonschema:"required,description=Order id"`
	Verbose bool   `json:"verbose,omitempty" jsonschema:"description=Include verbose order details"`
}

type lookupOrderOutput struct {
	Status string `json:"status"`
}

type panickingMarshaler struct{}

func (panickingMarshaler) MarshalJSON() ([]byte, error) { panic("marshal panic") }

type customRootInput struct{ Value string }

func (customRootInput) UnmarshalJSON([]byte) error { return nil }

type customNestedInput struct{ Value string }

func (*customNestedInput) UnmarshalJSON([]byte) error { return nil }

type textDecodedString string

func (*textDecodedString) UnmarshalText([]byte) error { return nil }

type textDecodedRootInput struct{ Value string }

func (textDecodedRootInput) UnmarshalText([]byte) error { return nil }

type inputWithTextDecodedString struct {
	Value textDecodedString `json:"value"`
}

type inputWithCustomNested struct {
	Direct *customNestedInput           `json:"direct"`
	List   []customNestedInput          `json:"list"`
	Map    map[string]customNestedInput `json:"map"`
	Array  [1]*customNestedInput        `json:"array"`
}

type recursiveInput struct {
	Next   *recursiveInput   `json:"next,omitempty"`
	Custom customNestedInput `json:"custom"`
}

type stringTaggedInput struct {
	Count int `json:"count,string"`
}

type ignoredCustomInput struct {
	hidden  customNestedInput
	Ignored customNestedInput `json:"-,string"`
	Value   string            `json:"value"`
}

type embeddedCustomInput struct {
	Custom customNestedInput `json:"custom"`
}

type inputWithEmbeddedCustom struct {
	embeddedCustomInput
}

type embeddedPlainInput struct {
	Value string `json:"value"`
}

type inputWithEmbeddedPlain struct {
	embeddedPlainInput
}

type inputWithEmbeddedPointer struct {
	*embeddedPlainInput
}

type cloudIncompatibleInput struct {
	Integer   int                `json:"integer"`
	Unsigned  uint               `json:"unsigned"`
	Nested    embeddedPlainInput `json:"nested"`
	Pointer   *string            `json:"pointer"`
	Array     [1]string          `json:"array"`
	Slice     []string           `json:"slice"`
	Map       map[string]any     `json:"map"`
	Interface any                `json:"interface"`
}

type cloudCompatibleInput struct {
	Text    string  `json:"text"`
	Enabled bool    `json:"enabled"`
	Score32 float32 `json:"score32"`
	Score64 float64 `json:"score64"`
}

type primitiveMetadataInput struct {
	Text    string  `json:"text" jsonschema:"required,description=Text value"`
	Enabled bool    `json:"enabled" jsonschema:"description=Enabled value"`
	Score   float64 `json:"score" jsonschema:"description=Score value"`
}

type patternInput struct {
	Value string `json:"value" jsonschema:"pattern=^[a-z]+$"`
}

type enumInput struct {
	Value string `json:"value" jsonschema:"enum=one,enum=two"`
}

type lengthInput struct {
	Value string `json:"value" jsonschema:"minLength=1,maxLength=10"`
}

type rangeInput struct {
	Value float64 `json:"value" jsonschema:"minimum=1,maximum=10"`
}

type rootOneOfInput struct {
	Value string `json:"value" jsonschema:"oneof_required=group"`
}

type duplicateJSONNameInput struct {
	First  string `json:"Second"`
	Second string
}

type duplicateJSONNameDifferentTypesInput struct {
	First  string `json:"Second"`
	Second float64
}

type uniqueJSONNameInput struct {
	First  string `json:"first"`
	Second bool
}

type caseFoldJSONNameInput struct {
	First  string `json:"value"`
	Second string `json:"VALUE"`
}

type invalidBackslashTagInput struct {
	Value string `json:"bad\\name"`
}

type invalidControlTagInput struct {
	Value string `json:"bad\tname"`
}

type validPunctuationTagInput struct {
	Value string `json:"!#$%&()*+-./:;<=>?@[]^_{|}~ name"`
}

func lookupOrderHandler(context.Context, Call[lookupOrderInput]) (lookupOrderOutput, error) {
	return lookupOrderOutput{Status: "paid"}, nil
}

func TestNewDerivesStrictReadOnlyDefinition(t *testing.T) {
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, lookupOrderHandler)
	if err != nil {
		t.Fatalf("new tool: %v", err)
	}
	definition := serverTool.Definition()
	if definition.Name() != "lookup_order" || definition.Version() != "1" || definition.Description() != "Look up order" {
		t.Fatalf("unexpected definition: %s %s %s", definition.Name(), definition.Version(), definition.Description())
	}

	schema := definition.InputSchema()
	wantSchema := `{"$schema":"https://json-schema.org/draft/2020-12/schema","additionalProperties":false,"properties":{"orderId":{"description":"Order id","type":"string"},"verbose":{"description":"Include verbose order details","type":"boolean"}},"required":["orderId"],"type":"object"}`
	if string(schema) != wantSchema {
		t.Fatalf("schema = %s, want %s", schema, wantSchema)
	}
	schema[0] = 'x'
	if got := serverTool.Definition().InputSchema()[0]; got == 'x' {
		t.Fatal("input schema shares mutable bytes")
	}
}

func TestNewValidatesMetadataAndInput(t *testing.T) {
	tests := []struct {
		name     string
		metadata Metadata
		want     error
	}{
		{name: "name", metadata: Metadata{Version: "1", Description: "description"}, want: ErrInvalidMetadata},
		{name: "version", metadata: Metadata{Name: "name", Description: "description"}, want: ErrInvalidMetadata},
		{name: "description", metadata: Metadata{Name: "name", Version: "1"}, want: ErrInvalidMetadata},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.metadata, lookupOrderHandler)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.want)
			}
		})
	}

	_, err := New(Metadata{Name: "bad", Version: "1", Description: "Bad"}, func(context.Context, Call[string]) (string, error) {
		return "", nil
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want errors.Is(_, ErrInvalidInput)", err)
	}
}

func TestNewRejectsCustomJSONInputDecoding(t *testing.T) {
	if _, err := New(Metadata{Name: "root", Version: "1", Description: "root"}, func(context.Context, Call[customRootInput]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("root error = %v", err)
	}
	if _, err := New(Metadata{Name: "nested", Version: "1", Description: "nested"}, func(context.Context, Call[inputWithCustomNested]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nested error = %v", err)
	}
}

func TestNewRejectsRecursiveInputTypes(t *testing.T) {
	if _, err := New(Metadata{Name: "recursive", Version: "1", Description: "recursive"}, func(context.Context, Call[recursiveInput]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("recursive error = %v", err)
	}
}

func TestNewRejectsJSONStringTagOption(t *testing.T) {
	_, err := New(Metadata{Name: "tagged", Version: "1", Description: "tagged"}, func(context.Context, Call[stringTaggedInput]) (struct{}, error) { return struct{}{}, nil })
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewSkipsJSONIgnoredInputFields(t *testing.T) {
	_, err := New(Metadata{Name: "ignored", Version: "1", Description: "ignored"}, func(context.Context, Call[ignoredCustomInput]) (struct{}, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatalf("ignored fields: %v", err)
	}
}

func TestNewValidatesPromotedFieldsFromUnexportedEmbeddedStructs(t *testing.T) {
	if _, err := New(Metadata{Name: "custom", Version: "1", Description: "custom"}, func(context.Context, Call[inputWithEmbeddedCustom]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("embedded custom decoder error = %v", err)
	}
	if _, err := New(Metadata{Name: "plain", Version: "1", Description: "plain"}, func(context.Context, Call[inputWithEmbeddedPlain]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("embedded struct error = %v", err)
	}
}

func TestNewRejectsTextUnmarshalerInput(t *testing.T) {
	if _, err := New(Metadata{Name: "root_text", Version: "1", Description: "root text"}, func(context.Context, Call[textDecodedRootInput]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("root text decoder error = %v", err)
	}
	if _, err := New(Metadata{Name: "field_text", Version: "1", Description: "field text"}, func(context.Context, Call[inputWithTextDecodedString]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("field text decoder error = %v", err)
	}
}

func TestNewValidatesJSONTagNames(t *testing.T) {
	if _, err := New(Metadata{Name: "backslash", Version: "1", Description: "backslash"}, func(context.Context, Call[invalidBackslashTagInput]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("backslash tag error = %v", err)
	}
	if _, err := New(Metadata{Name: "control", Version: "1", Description: "control"}, func(context.Context, Call[invalidControlTagInput]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("control tag error = %v", err)
	}
	if _, err := New(Metadata{Name: "punctuation", Version: "1", Description: "punctuation"}, func(context.Context, Call[validPunctuationTagInput]) (struct{}, error) { return struct{}{}, nil }); err != nil {
		t.Fatalf("valid punctuation tag: %v", err)
	}
}

func TestNewRejectsCloudIncompatibleInputFields(t *testing.T) {
	if _, err := New(Metadata{Name: "incompatible", Version: "1", Description: "incompatible"}, func(context.Context, Call[cloudIncompatibleInput]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("incompatible fields error = %v", err)
	}
	if _, err := New(Metadata{Name: "compatible", Version: "1", Description: "compatible"}, func(context.Context, Call[cloudCompatibleInput]) (struct{}, error) { return struct{}{}, nil }); err != nil {
		t.Fatalf("compatible fields: %v", err)
	}
}

func TestNewAcceptsPrimitiveSchemaMetadata(t *testing.T) {
	_, err := New(Metadata{Name: "metadata", Version: "1", Description: "metadata"}, func(context.Context, Call[primitiveMetadataInput]) (struct{}, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
}

func TestNewRejectsUnsupportedGeneratedSchemaKeywords(t *testing.T) {
	for _, test := range []struct {
		name string
		new  func() error
	}{
		{name: "pattern", new: func() error {
			_, err := New(Metadata{Name: "pattern", Version: "1", Description: "pattern"}, func(context.Context, Call[patternInput]) (struct{}, error) { return struct{}{}, nil })
			return err
		}},
		{name: "enum", new: func() error {
			_, err := New(Metadata{Name: "enum", Version: "1", Description: "enum"}, func(context.Context, Call[enumInput]) (struct{}, error) { return struct{}{}, nil })
			return err
		}},
		{name: "length", new: func() error {
			_, err := New(Metadata{Name: "length", Version: "1", Description: "length"}, func(context.Context, Call[lengthInput]) (struct{}, error) { return struct{}{}, nil })
			return err
		}},
		{name: "range", new: func() error {
			_, err := New(Metadata{Name: "range", Version: "1", Description: "range"}, func(context.Context, Call[rangeInput]) (struct{}, error) { return struct{}{}, nil })
			return err
		}},
		{name: "root oneOf", new: func() error {
			_, err := New(Metadata{Name: "root_oneof", Version: "1", Description: "root oneof"}, func(context.Context, Call[rootOneOfInput]) (struct{}, error) { return struct{}{}, nil })
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.new(); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want errors.Is(_, ErrInvalidInput)", err)
			}
		})
	}
}

func TestNewRejectsUnnamedRootInput(t *testing.T) {
	_, err := New(Metadata{Name: "unnamed", Version: "1", Description: "unnamed"}, func(context.Context, Call[struct {
		Value string `json:"value"`
	}]) (struct{}, error) {
		return struct{}{}, nil
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewRejectsDuplicateEffectiveJSONPropertyNames(t *testing.T) {
	if _, err := New(Metadata{Name: "duplicate", Version: "1", Description: "duplicate"}, func(context.Context, Call[duplicateJSONNameInput]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("same-type duplicate error = %v", err)
	}
	if _, err := New(Metadata{Name: "duplicate_types", Version: "1", Description: "duplicate types"}, func(context.Context, Call[duplicateJSONNameDifferentTypesInput]) (struct{}, error) {
		return struct{}{}, nil
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("different-type duplicate error = %v", err)
	}
	if _, err := New(Metadata{Name: "unique", Version: "1", Description: "unique"}, func(context.Context, Call[uniqueJSONNameInput]) (struct{}, error) { return struct{}{}, nil }); err != nil {
		t.Fatalf("unique fields: %v", err)
	}
	if _, err := New(Metadata{Name: "case_fold", Version: "1", Description: "case fold"}, func(context.Context, Call[caseFoldJSONNameInput]) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("case-fold duplicate error = %v", err)
	}
}

func TestNewRejectsUnexportedAnonymousPointerField(t *testing.T) {
	_, err := New(Metadata{Name: "embedded_pointer", Version: "1", Description: "embedded pointer"}, func(context.Context, Call[inputWithEmbeddedPointer]) (struct{}, error) { return struct{}{}, nil })
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewValidatesToolNameRoutingIdentity(t *testing.T) {
	for _, name := range []string{"Az_09", strings.Repeat("a", 128)} {
		if _, err := New(Metadata{Name: name, Version: "version with spaces", Description: "valid"}, lookupOrderHandler); err != nil {
			t.Fatalf("valid name length %d: %v", len(name), err)
		}
	}
	for _, name := range []string{"has space", "has.dot", "has-hyphen", "_leading", "trailing_", "double__underscore", "café", strings.Repeat("a", 129)} {
		_, err := New(Metadata{Name: name, Version: "1", Description: "invalid"}, lookupOrderHandler)
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("name %q error = %v", name, err)
		}
	}
}

func TestNewValidatesToolVersionLength(t *testing.T) {
	if _, err := New(Metadata{Name: "name", Version: strings.Repeat("é", 128), Description: strings.Repeat("界", 4096)}, lookupOrderHandler); err != nil {
		t.Fatalf("128-character version: %v", err)
	}
	if _, err := New(Metadata{Name: "name", Version: strings.Repeat("😀", 64), Description: strings.Repeat("😀", 2048)}, lookupOrderHandler); err != nil {
		t.Fatalf("UTF-16 boundary: %v", err)
	}
	_, err := New(Metadata{Name: "name", Version: strings.Repeat("é", 129), Description: "invalid"}, lookupOrderHandler)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("129-character version error = %v", err)
	}
	_, err = New(Metadata{Name: "name", Version: "1", Description: strings.Repeat("界", 4097)}, lookupOrderHandler)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("4097-character description error = %v", err)
	}
	for _, metadata := range []Metadata{
		{Name: "name", Version: strings.Repeat("😀", 128), Description: "invalid"},
		{Name: "name", Version: "1", Description: strings.Repeat("😀", 2049)},
	} {
		if _, err := New(metadata, lookupOrderHandler); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("astral boundary error = %v", err)
		}
	}
}

func TestDefinitionSchemaIsJSON(t *testing.T) {
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, lookupOrderHandler)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(serverTool.Definition().InputSchema()) {
		t.Fatal("input schema is not valid JSON")
	}
}

func TestExecuteDecodesTypedInputAndSerializesOutput(t *testing.T) {
	var received Call[lookupOrderInput]
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, func(_ context.Context, call Call[lookupOrderInput]) (lookupOrderOutput, error) {
		received = call
		return lookupOrderOutput{Status: "paid"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	output, err := serverTool.Execute(context.Background(), json.RawMessage(`{"orderId":"ord_1"}`), Subject{UserID: "user_1"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if received.Input.OrderID != "ord_1" || received.Subject.UserID != "user_1" {
		t.Fatalf("unexpected call: %+v", received)
	}
	if string(output) != `{"status":"paid"}` {
		t.Fatalf("output = %s", output)
	}
}

func TestExecuteRejectsUnknownInputField(t *testing.T) {
	called := false
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, func(context.Context, Call[lookupOrderInput]) (lookupOrderOutput, error) {
		called = true
		return lookupOrderOutput{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = serverTool.Execute(context.Background(), json.RawMessage(`{"orderId":"ord_1","unknown":true}`), Subject{})
	if err == nil || called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
}

func TestExecuteRequiresExactUniqueInputKeys(t *testing.T) {
	called := false
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, func(context.Context, Call[lookupOrderInput]) (lookupOrderOutput, error) {
		called = true
		return lookupOrderOutput{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []json.RawMessage{
		json.RawMessage(`{"OrderId":"ord_1"}`),
		json.RawMessage(`{"orderId":"ord_1","orderId":"ord_2"}`),
	} {
		called = false
		if _, err := serverTool.Execute(context.Background(), input, Subject{}); err == nil || called {
			t.Fatalf("input=%s err=%v called=%v", input, err, called)
		}
	}
	if _, err := serverTool.Execute(context.Background(), json.RawMessage(`{"orderId":"ord_1"}`), Subject{}); err != nil || !called {
		t.Fatalf("exact key err=%v called=%v", err, called)
	}
}

func TestExecuteRejectsTrailingJSON(t *testing.T) {
	called := false
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, func(context.Context, Call[lookupOrderInput]) (lookupOrderOutput, error) {
		called = true
		return lookupOrderOutput{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = serverTool.Execute(context.Background(), json.RawMessage(`{"orderId":"ord_1"} {"orderId":"ord_2"}`), Subject{})
	if err == nil || called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
}

func TestValidateInputKeysAcceptsScalarValues(t *testing.T) {
	large := strings.Repeat("x", 1<<20)
	for _, input := range []string{
		`{"text":"exact","enabled":true,"score":-1.25e+30}`,
		`{"text":"` + large + `"}`,
	} {
		if err := validateInputKeys(json.RawMessage(input), map[string]struct{}{"text": {}, "enabled": {}, "score": {}}); err != nil {
			t.Fatalf("input rejected: %v", err)
		}
	}
}

func TestValidateInputKeysRejectsInvalidInput(t *testing.T) {
	allowed := map[string]struct{}{"text": {}, "enabled": {}, "score": {}}
	for _, input := range []string{
		`null`,
		`[]`,
		`{"text":null}`,
		`{"text":{}}`,
		`{"text":[]}`,
		`{"text":"value"`,
		`{"text":"value"} {}`,
		`{"text":"one","te\u0078t":"two"}`,
		`{"Text":"value"}`,
		`{"unknown":"value"}`,
	} {
		if err := validateInputKeys(json.RawMessage(input), allowed); err == nil {
			t.Fatalf("input accepted: %s", input)
		}
	}
}

func TestExecuteRejectsNullInput(t *testing.T) {
	called := false
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, func(context.Context, Call[lookupOrderInput]) (lookupOrderOutput, error) {
		called = true
		return lookupOrderOutput{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := serverTool.Execute(context.Background(), json.RawMessage(`null`), Subject{}); err == nil || called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
}

func TestExecuteAllowsNullOutput(t *testing.T) {
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, func(context.Context, Call[lookupOrderInput]) (*lookupOrderOutput, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	output, err := serverTool.Execute(context.Background(), json.RawMessage(`{"orderId":"ord_1"}`), Subject{})
	if err != nil || string(output) != "null" {
		t.Fatalf("output = %s, err = %v", output, err)
	}
}

func TestExecuteRejectsNonJSONOutput(t *testing.T) {
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, func(context.Context, Call[lookupOrderInput]) (chan int, error) {
		return make(chan int), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := serverTool.Execute(context.Background(), json.RawMessage(`{"orderId":"ord_1"}`), Subject{}); err == nil {
		t.Fatal("expected output serialization error")
	}
}

func TestExecuteRecoversPanicWithDiagnostics(t *testing.T) {
	serverTool, err := New(Metadata{Name: "lookup_order", Version: "1", Description: "Look up order"}, func(context.Context, Call[lookupOrderInput]) (lookupOrderOutput, error) {
		panic("database exploded")
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = serverTool.Execute(context.Background(), json.RawMessage(`{"orderId":"ord_1"}`), Subject{})
	if err == nil || !strings.Contains(err.Error(), "database exploded") || !strings.Contains(err.Error(), "goroutine") {
		t.Fatalf("panic diagnostics = %v", err)
	}
}

func TestNewErrorNormalizesAndCopiesDetails(t *testing.T) {
	details := map[string]any{"field": "orderId"}
	publicErr := NewError("", "", details)
	details["field"] = "changed"

	if publicErr.Code() != UnknownErrorCode || publicErr.Message() != UnknownErrorMessage || publicErr.Error() != UnknownErrorMessage {
		t.Fatalf("unexpected public error: code=%q message=%q error=%q", publicErr.Code(), publicErr.Message(), publicErr.Error())
	}
	got := publicErr.Details()
	if got["field"] != "orderId" {
		t.Fatalf("details = %#v", got)
	}
	got["field"] = "changed again"
	if publicErr.Details()["field"] != "orderId" {
		t.Fatal("details accessor returned mutable state")
	}
}

func TestNewErrorDeepCopiesJSONDetails(t *testing.T) {
	nested := map[string]any{"map": map[string]any{"number": json.Number("18446744073709551615")}, "slice": []any{map[string]any{"value": "original"}}}
	publicErr := NewError("safe", "safe", nested)
	nested["map"].(map[string]any)["number"] = json.Number("1")
	nested["slice"].([]any)[0].(map[string]any)["value"] = "changed"

	got := publicErr.Details()
	if got["map"].(map[string]any)["number"] != json.Number("18446744073709551615") || got["slice"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatalf("details = %#v", got)
	}
	got["map"].(map[string]any)["number"] = json.Number("2")
	if publicErr.Details()["map"].(map[string]any)["number"] != json.Number("18446744073709551615") {
		t.Fatal("Details returned nested mutable state")
	}
}

func TestNewErrorDropsNonJSONDetails(t *testing.T) {
	publicErr := NewError("safe", "safe", map[string]any{"bad": make(chan int)})
	if publicErr.Code() != UnknownErrorCode || publicErr.Message() != UnknownErrorMessage || publicErr.Details() != nil {
		t.Fatalf("error = %q/%q details=%#v", publicErr.Code(), publicErr.Message(), publicErr.Details())
	}
}

func TestNewErrorNormalizesPanickingJSONDetails(t *testing.T) {
	publicErr := NewError("safe", "safe", map[string]any{"bad": panickingMarshaler{}})
	if publicErr.Code() != UnknownErrorCode || publicErr.Message() != UnknownErrorMessage || publicErr.Details() != nil {
		t.Fatalf("error = %q/%q details=%#v", publicErr.Code(), publicErr.Message(), publicErr.Details())
	}
}

func TestErrorSupportsStandardWrapping(t *testing.T) {
	publicErr := NewError("order.not_found", "Order not found", nil)
	wrapped := fmt.Errorf("lookup failed: %w", publicErr)
	var target *Error
	if !errors.As(wrapped, &target) || target != publicErr {
		t.Fatalf("errors.As target = %#v", target)
	}
}

func TestNewNamespaceValidatesAndCopiesTools(t *testing.T) {
	searchV1, err := New(Metadata{Name: "search", Version: "1", Description: "Search v1"}, lookupOrderHandler)
	if err != nil {
		t.Fatal(err)
	}
	searchV2, err := New(Metadata{Name: "search", Version: "2", Description: "Search v2"}, lookupOrderHandler)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name  string
		ns    string
		tools []Tool
		want  error
	}{
		{name: "empty name", tools: []Tool{searchV1}, want: ErrInvalidNamespace},
		{name: "empty tools", ns: "crm", want: ErrInvalidNamespace},
		{name: "space", ns: "sales ops", tools: []Tool{searchV1}, want: ErrInvalidNamespace},
		{name: "dot", ns: "sales.ops", tools: []Tool{searchV1}, want: ErrInvalidNamespace},
		{name: "hyphen", ns: "sales-ops", tools: []Tool{searchV1}, want: ErrInvalidNamespace},
		{name: "leading underscore", ns: "_sales", tools: []Tool{searchV1}, want: ErrInvalidNamespace},
		{name: "trailing underscore", ns: "sales_", tools: []Tool{searchV1}, want: ErrInvalidNamespace},
		{name: "double underscore", ns: "sales__ops", tools: []Tool{searchV1}, want: ErrInvalidNamespace},
		{name: "non ascii", ns: "café", tools: []Tool{searchV1}, want: ErrInvalidNamespace},
		{name: "too long", ns: strings.Repeat("n", 257), tools: []Tool{searchV1}, want: ErrInvalidNamespace},
		{name: "duplicate identity", ns: "crm", tools: []Tool{searchV1, searchV1}, want: ErrDuplicateTool},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewNamespace(tt.ns, tt.tools...)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.want)
			}
		})
	}
	for _, name := range []string{"Az_09", strings.Repeat("n", 256)} {
		if _, err := NewNamespace(name, searchV1); err != nil {
			t.Fatalf("valid namespace length %d: %v", len(name), err)
		}
	}

	namespace, err := NewNamespace("crm", searchV1, searchV2)
	if err != nil {
		t.Fatalf("multiple versions: %v", err)
	}
	if namespace.Name() != "crm" || len(namespace.Tools()) != 2 {
		t.Fatalf("unexpected namespace: name=%q tools=%d", namespace.Name(), len(namespace.Tools()))
	}
	tools := namespace.Tools()
	tools[0] = Tool{}
	if namespace.Tools()[0].Definition().Name() != "search" {
		t.Fatal("tools accessor returned mutable slice")
	}
}
