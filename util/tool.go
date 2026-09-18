package util

import (
	"context"
	"fmt"

	"encoding/json/v2"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/speed2exe/ag/tool"
)

type ToolForJsonInput[Input any] struct {
	Name        string
	Description *string
	Call        func(ctx context.Context, input *Input) ([]byte, error)
}

func (t *ToolForJsonInput[Input]) Compile() (*tool.Tool, error) {
	schema, err := SchemaForInput[Input]()
	if err != nil {
		return nil, fmt.Errorf("failed to generate schema for tool %s: %w", t.Name, err)
	}
	return &tool.Tool{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: schema,
		Call: func(ctx context.Context, args []byte) ([]byte, error) {
			var input Input
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("failed to unmarshal tool input: %w", err)
			}
			output, err := t.Call(ctx, &input)
			if err != nil {
				return nil, fmt.Errorf("tool call failed: %w", err)
			}
			return output, nil
		},
	}, nil
}

func (t *ToolForJsonInput[Input]) MustCompile() *tool.Tool {
	tool, err := t.Compile()
	if err != nil {
		panic(fmt.Errorf("failed to compile tool: %w", err))
	}
	return tool
}

type ToolForJsonInputOutput[Input, Output any] struct {
	Name        string
	Description *string
	Call        func(ctx context.Context, input *Input) (*Output, error)
}

func (t *ToolForJsonInputOutput[Input, Output]) Compile() (*tool.Tool, error) {
	call := func(ctx context.Context, input *Input) ([]byte, error) {
		output, err := t.Call(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("tool call failed: %w", err)
		}
		bs, err := json.Marshal(output)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tool output: %w", err)
		}
		return bs, nil
	}

	return (&ToolForJsonInput[Input]{
		Name:        t.Name,
		Description: t.Description,
		Call:        call,
	}).Compile()
}

func (t *ToolForJsonInputOutput[Input, Output]) MustCompile() *tool.Tool {
	tool, err := t.Compile()
	if err != nil {
		panic(fmt.Errorf("failed to compile tool: %w", err))
	}
	return tool
}

func SchemaForInput[Input any]() ([]byte, error) {
	s, err := jsonschema.For[Input](nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate json schema: %w", err)
	}
	bs, err := s.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal json schema: %w", err)
	}
	return bs, nil
}

func MustSchemaForInput[Input any]() []byte {
	schema, err := SchemaForInput[Input]()
	if err != nil {
		panic(fmt.Errorf("failed to generate json schema: %w", err))
	}
	return schema
}
