package util

import (
	"context"
	"encoding/json/v2"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/speed2exe/ag/tool"
)

func NewJSONTool[Input, Output any](
	name, description string,
	call func(ctx context.Context, input *Input) (*Output, error),
) *tool.Tool {
	s, err := jsonschema.For[Input](nil)
	if err != nil {
		panic(fmt.Errorf("failed to generate json schema for tool %s: %w", name, err))
	}
	schema, err := s.MarshalJSON()
	if err != nil {
		panic(fmt.Errorf("failed to marshal json schema for tool %s: %w", name, err))
	}
	return &tool.Tool{
		Name:        name,
		Description: &description,
		InputSchema: schema,
		Call: func(ctx context.Context, args []byte) ([]byte, error) {
			var input Input
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("failed to unmarshal tool input: %w", err)
			}
			output, err := call(ctx, &input)
			if err != nil {
				return nil, fmt.Errorf("tool call failed: %w", err)
			}
			bs, err := json.Marshal(output)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal tool output: %w", err)
			}
			return bs, nil
		},
	}
}
