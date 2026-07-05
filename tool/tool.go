package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
)

const (
	InternalErrorCode    = "tool.internal_error"
	InternalErrorMessage = "tool execution failed"
)

type Definition struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

type Manifest struct {
	Namespace   string
	Definitions []Definition
}

type Subject struct {
	UserID string `json:"userId"`
}

type Call[T any] struct {
	Namespace string
	Name      string
	Version   string
	Input     T
	Subject   Subject
	Deadline  time.Time
}

type Handler[In any] func(context.Context, Call[In]) Out

type Out struct {
	Result any
	Error  *Error
}

type Error struct {
	Code    string
	Message string
	Details map[string]any
}

func OK(result any) Out {
	return Out{Result: result}
}

func Err(code string, message string, details map[string]any) Out {
	if strings.TrimSpace(code) == "" {
		code = InternalErrorCode
	}
	if strings.TrimSpace(message) == "" {
		message = InternalErrorMessage
	}
	return Out{Error: &Error{Code: code, Message: message, Details: details}}
}

func Define[T any](name string, version string, description string) (Definition, error) {
	schema, err := SchemaOf[T]()
	if err != nil {
		return Definition{}, err
	}
	return Definition{Name: name, Version: version, Description: description, InputSchema: schema}, nil
}

func SchemaOf[T any]() (any, error) {
	var input T
	typeOfInput := reflect.TypeOf(input)
	if typeOfInput == nil || typeOfInput.Kind() != reflect.Struct {
		return nil, fmt.Errorf("tool: typed tool input must be a struct")
	}

	reflector := jsonschema.Reflector{Anonymous: true, ExpandedStruct: true, DoNotReference: true}
	schema := reflector.Reflect(&input)
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}
