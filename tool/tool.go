package tool

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"runtime/debug"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	UnknownErrorCode    = "unknown"
	UnknownErrorMessage = "Tool execution failed"
)

var (
	ErrInvalidMetadata  = errors.New("tool: invalid metadata")
	ErrInvalidInput     = errors.New("tool: invalid input type")
	ErrInvalidNamespace = errors.New("tool: invalid namespace")
	ErrDuplicateTool    = errors.New("tool: duplicate tool")
)

type Metadata struct {
	Name        string
	Version     string
	Description string
}

type Definition struct {
	name        string
	version     string
	description string
	inputSchema json.RawMessage
}

func (d Definition) Name() string { return d.name }

func (d Definition) Version() string { return d.version }

func (d Definition) Description() string { return d.description }

func (d Definition) InputSchema() json.RawMessage {
	return append(json.RawMessage(nil), d.inputSchema...)
}

type Subject struct {
	UserID string `json:"userId"`
}

type CallContext struct {
	SessionLabels map[string]string
}

type Call[Input any] struct {
	Input   Input
	Subject Subject
	Context CallContext
}

type Handler[Input, Output any] func(context.Context, Call[Input]) (Output, error)

type Tool struct {
	definition Definition
	execute    func(context.Context, json.RawMessage, Subject, CallContext) (json.RawMessage, error)
}

func New[Input, Output any](metadata Metadata, handler Handler[Input, Output]) (Tool, error) {
	if !validRoutingName(metadata.Name, 128) || strings.TrimSpace(metadata.Version) == "" || utf16Length(metadata.Version) > 128 || strings.TrimSpace(metadata.Description) == "" || utf16Length(metadata.Description) > 4096 || handler == nil {
		return Tool{}, fmt.Errorf("%w: name, version, description, and handler are required", ErrInvalidMetadata)
	}
	schema, allowed, err := describeInput[Input]()
	if err != nil {
		return Tool{}, err
	}
	return Tool{definition: Definition{
		name:        metadata.Name,
		version:     metadata.Version,
		description: metadata.Description,
		inputSchema: schema,
	}, execute: func(ctx context.Context, input json.RawMessage, subject Subject, callContext CallContext) (output json.RawMessage, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("tool: handler panic: %v\n%s", recovered, debug.Stack())
			}
		}()
		if strings.TrimSpace(string(input)) == "null" {
			return nil, errors.New("tool: decode input: expected object, got null")
		}
		if err := validateInputKeys(input, allowed); err != nil {
			return nil, fmt.Errorf("tool: decode input: %w", err)
		}
		var decoded Input
		decoder := json.NewDecoder(strings.NewReader(string(input)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("tool: decode input: %w", err)
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return nil, errors.New("tool: decode input: trailing JSON")
		}
		result, err := handler(ctx, Call[Input]{Input: decoded, Subject: subject, Context: CallContext{SessionLabels: maps.Clone(callContext.SessionLabels)}})
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("tool: encode output: %w", err)
		}
		return encoded, nil
	}}, nil
}

func (t Tool) Definition() Definition { return t.definition }

func (t Tool) Execute(ctx context.Context, input json.RawMessage, subject Subject) (json.RawMessage, error) {
	if t.execute == nil {
		return nil, errors.New("tool: invalid tool")
	}
	return t.execute(ctx, input, subject, CallContext{})
}

func (t Tool) ExecuteWithContext(ctx context.Context, input json.RawMessage, subject Subject, callContext CallContext) (json.RawMessage, error) {
	if t.execute == nil {
		return nil, errors.New("tool: invalid tool")
	}
	return t.execute(ctx, input, subject, callContext)
}

type Error struct {
	code    string
	message string
	details map[string]any
}

func NewError(code string, message string, details map[string]any) *Error {
	cloned := cloneMap(details)
	if details != nil && cloned == nil {
		return &Error{code: UnknownErrorCode, message: UnknownErrorMessage}
	}
	if strings.TrimSpace(code) == "" {
		code = UnknownErrorCode
	}
	if strings.TrimSpace(message) == "" {
		message = UnknownErrorMessage
	}
	return &Error{code: code, message: message, details: cloned}
}

func (e *Error) Error() string { return e.message }

func (e *Error) Code() string { return e.code }

func (e *Error) Message() string { return e.message }

func (e *Error) Details() map[string]any { return cloneMap(e.details) }

type Namespace struct {
	name  string
	tools []Tool
}

func NewNamespace(name string, tools ...Tool) (Namespace, error) {
	if !validRoutingName(name, 256) || len(tools) == 0 {
		return Namespace{}, fmt.Errorf("%w: name and tools are required", ErrInvalidNamespace)
	}
	seen := make(map[string]struct{}, len(tools))
	for _, serverTool := range tools {
		definition := serverTool.Definition()
		if definition.Name() == "" || serverTool.execute == nil {
			return Namespace{}, fmt.Errorf("%w: contains invalid tool", ErrInvalidNamespace)
		}
		key := definition.Name() + "\x00" + definition.Version()
		if _, ok := seen[key]; ok {
			return Namespace{}, fmt.Errorf("%w: %s@%s", ErrDuplicateTool, definition.Name(), definition.Version())
		}
		seen[key] = struct{}{}
	}
	return Namespace{name: name, tools: append([]Tool(nil), tools...)}, nil
}

func (n Namespace) Name() string { return n.name }

func (n Namespace) Tools() []Tool { return append([]Tool(nil), n.tools...) }

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func validRoutingName(name string, max int) bool {
	if name == "" || utf8.RuneCountInString(name) > max || name[0] == '_' || name[len(name)-1] == '_' || strings.Contains(name, "__") {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if !('A' <= c && c <= 'Z') && !('a' <= c && c <= 'z') && !('0' <= c && c <= '9') && c != '_' {
			return false
		}
	}
	return true
}

func cloneMap(source map[string]any) (cloned map[string]any) {
	if source == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			cloned = nil
		}
	}()
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&cloned); err != nil {
		return nil
	}
	return cloned
}

var (
	jsonUnmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

func customJSONDecoding(t reflect.Type) bool {
	return t.Implements(jsonUnmarshalerType) || reflect.PointerTo(t).Implements(jsonUnmarshalerType) ||
		t.Implements(textUnmarshalerType) || reflect.PointerTo(t).Implements(textUnmarshalerType)
}

func describeInput[Input any]() (json.RawMessage, map[string]struct{}, error) {
	t := reflect.TypeOf((*Input)(nil)).Elem()
	if t.Kind() != reflect.Struct || t.Name() == "" {
		return nil, nil, fmt.Errorf("%w: must be a named struct", ErrInvalidInput)
	}
	if customJSONDecoding(t) {
		return nil, nil, fmt.Errorf("%w: custom JSON decoding is not supported", ErrInvalidInput)
	}

	properties := make(map[string]map[string]string, t.NumField())
	allowed := make(map[string]struct{}, t.NumField())
	required := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		field := t.Field(i)
		if field.Anonymous {
			return nil, nil, fmt.Errorf("%w: anonymous fields are not supported", ErrInvalidInput)
		}
		jsonTag := strings.Split(field.Tag.Get("json"), ",")
		if field.PkgPath != "" || jsonTag[0] == "-" {
			continue
		}
		name := field.Name
		if jsonTag[0] != "" {
			if !validJSONTagName(jsonTag[0]) {
				return nil, nil, fmt.Errorf("%w: invalid JSON field name %q", ErrInvalidInput, jsonTag[0])
			}
			name = jsonTag[0]
		}
		for _, option := range jsonTag[1:] {
			if option != "" && option != "omitempty" && option != "omitzero" {
				return nil, nil, fmt.Errorf("%w: unsupported json option %q", ErrInvalidInput, option)
			}
		}
		for existing := range allowed {
			if strings.EqualFold(existing, name) {
				return nil, nil, fmt.Errorf("%w: duplicate JSON field name %q", ErrInvalidInput, name)
			}
		}
		allowed[name] = struct{}{}

		if customJSONDecoding(field.Type) {
			return nil, nil, fmt.Errorf("%w: field %q custom JSON decoding is not supported", ErrInvalidInput, name)
		}
		propertyType := ""
		switch field.Type.Kind() {
		case reflect.String:
			propertyType = "string"
		case reflect.Bool:
			propertyType = "boolean"
		case reflect.Float32, reflect.Float64:
			propertyType = "number"
		default:
			return nil, nil, fmt.Errorf("%w: field %q has unsupported type", ErrInvalidInput, name)
		}
		property := map[string]string{"type": propertyType}
		if err := parseSchemaTag(field.Tag.Get("jsonschema"), name, property, &required); err != nil {
			return nil, nil, err
		}
		properties[name] = property
	}

	schema := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"additionalProperties": false,
		"properties":           properties,
		"type":                 "object",
	}
	if len(required) != 0 {
		schema["required"] = required
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, nil, fmt.Errorf("tool: encode input schema: %w", err)
	}
	return encoded, allowed, nil
}

func parseSchemaTag(tag, name string, property map[string]string, required *[]string) error {
	for _, option := range strings.Split(tag, ",") {
		switch {
		case option == "required":
			*required = append(*required, name)
		case strings.HasPrefix(option, "description="):
			property["description"] = strings.TrimPrefix(option, "description=")
		case option != "":
			return fmt.Errorf("%w: field %q has unsupported jsonschema option %q", ErrInvalidInput, name, option)
		}
	}
	return nil
}

func validJSONTagName(name string) bool {
	for _, c := range name {
		if strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c) || unicode.IsLetter(c) || unicode.IsDigit(c) {
			continue
		}
		return false
	}
	return name != ""
}

func validateInputKeys(input json.RawMessage, allowed map[string]struct{}) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("expected object")
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name := token.(string)
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown field %q", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate field %q", name)
		}
		seen[name] = struct{}{}
		value, err := decoder.Token()
		if err != nil {
			return err
		}
		if value == nil {
			return fmt.Errorf("field %q must not be null", name)
		}
		if _, nested := value.(json.Delim); nested {
			return fmt.Errorf("field %q must be a scalar", name)
		}
	}
	if token, err := decoder.Token(); err != nil {
		return err
	} else if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return errors.New("expected object end")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
