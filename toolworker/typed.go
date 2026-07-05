package toolworker

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/aimount/aimount-go/tool"
)

func Handle[In any](worker *Worker, definition tool.Definition, handler tool.Handler[In]) error {
	if worker == nil {
		return errInvalidDefinition
	}
	if handler == nil {
		return errInvalidDefinition
	}
	return worker.handle(definition, func(ctx context.Context, call call) (outcome, error) {
		typedInput, err := decodeTypedInput[In](call.inputRaw, call.Input)
		if err != nil {
			return internalFailure(), nil
		}
		out := handler(ctx, tool.Call[In]{
			Namespace: call.Namespace,
			Name:      call.Name,
			Version:   call.Version,
			Input:     typedInput,
			Subject:   call.Subject,
			Deadline:  call.Deadline,
		})
		return outcomeFromOut(out), nil
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

func outcomeFromOut(out tool.Out) outcome {
	if out.Error != nil {
		return failed(out.Error.Code, out.Error.Message, out.Error.Details)
	}
	return succeeded(out.Result)
}
