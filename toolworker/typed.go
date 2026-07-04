package toolworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
)

type TypedCall[T any] struct {
	Namespace string
	Name      string
	Version   string
	Input     T
	Subject   Subject
	Deadline  time.Time
}

type TypedHandler[In any, Out any] func(context.Context, TypedCall[In]) (Out, error)

type ToolError struct {
	Code    string
	Message string
	Details map[string]any
}

func NewToolError(code string, message string, details map[string]any) error {
	if strings.TrimSpace(code) == "" {
		code = "unknown"
	}
	if strings.TrimSpace(message) == "" {
		message = "Tool execution failed"
	}
	return ToolError{Code: code, Message: message, Details: details}
}

func (e ToolError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func SchemaOf[T any]() (any, error) {
	var input T
	typeOfInput := reflect.TypeOf(input)
	if typeOfInput == nil || typeOfInput.Kind() != reflect.Struct {
		return nil, fmt.Errorf("toolworker: typed tool input must be a struct")
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

func Handle[In any, Out any](worker *Worker, name string, definition Definition, handler TypedHandler[In, Out]) error {
	if worker == nil {
		return errInvalidDefinition
	}
	if handler == nil {
		return errInvalidDefinition
	}
	schema, err := SchemaOf[In]()
	if err != nil {
		return err
	}
	definition.InputSchema = schema
	return worker.Handle(name, definition, func(ctx context.Context, call Call) (Outcome, error) {
		typedInput, err := decodeTypedInput[In](call.inputRaw, call.Input)
		if err != nil {
			return worker.mapError(err), nil
		}
		output, err := handler(ctx, TypedCall[In]{
			Namespace: call.Namespace,
			Name:      call.Name,
			Version:   call.Version,
			Input:     typedInput,
			Subject:   call.Subject,
			Deadline:  call.Deadline,
		})
		if err != nil {
			return worker.mapError(err), nil
		}
		return Succeeded(output), nil
	})
}

func decodeTypedInput[T any](raw json.RawMessage, input map[string]any) (T, error) {
	var decoded T
	encoded := []byte(raw)
	if len(encoded) == 0 {
		var err error
		encoded, err = json.Marshal(input)
		if err != nil {
			return decoded, err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return decoded, err
	}
	return decoded, nil
}

func outcomeForToolError(err error) (Outcome, bool) {
	var toolErr ToolError
	if !errors.As(err, &toolErr) {
		return Outcome{}, false
	}
	return Failed(toolErr.Code, toolErr.Message, toolErr.Details), true
}
