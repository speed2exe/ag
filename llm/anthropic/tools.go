package anthropic

import (
	"context"
	"fmt"

	"encoding/json/v2"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/util"
)

// CallTool implements [llm.ToolCaller].
func (c *Client) CallTool(ctx context.Context, toolName string, args string) (string, bool, error) {
	_, isAnTool := anToolsByName[toolName]
	if !isAnTool {
		return "", false, nil
	}
	return consts.LlmInternalToolHandling, true, nil
}

var (
	anTools = []anTool{
		loadImagesByUrl().compile(),
		loadImagesByFile().compile(),
		// ...
	}
	anToolsByName = func() map[string]anTool {
		m := make(map[string]anTool, len(anTools))
		for _, tool := range anTools {
			m[tool.name] = tool
		}
		return m
	}()
	anApiTools = func() []anthropic.ToolUnionParam {
		tools := make([]anthropic.ToolUnionParam, len(anTools))
		for i, tool := range anTools {
			tools[i] = anthropic.ToolUnionParam{
				OfTool: &anthropic.ToolParam{
					Name:        tool.name,
					Description: param.Opt[string]{Value: tool.description},
					InputSchema: tool.schema,
				},
			}
			anToolsByName[tool.name] = tool
		}
		return tools
	}()
)

type anToolTyped[Input any] struct {
	name        string
	description string
	call        func(ctx context.Context, callId string, input Input) (anthropic.ContentBlockParamUnion, error)
}

type anTool struct {
	name        string
	description string
	schema      anthropic.ToolInputSchemaParam
	call        func(ctx context.Context, callId, args string) (anthropic.ContentBlockParamUnion, error)
}

func (a anToolTyped[Input]) compile() anTool {
	schema, err := toolInputSchemaFor[Input]()
	if err != nil {
		panic(fmt.Sprintf("failed to generate tool input schema for tool %s: %v", a.name, err))
	}
	return anTool{
		name:        a.name,
		description: a.description,
		schema:      schema,
		call: func(ctx context.Context, callId, args string) (anthropic.ContentBlockParamUnion, error) {
			var input Input
			if err := json.Unmarshal([]byte(args), &input); err != nil {
				return anthropic.ContentBlockParamUnion{}, fmt.Errorf("failed to unmarshal tool input for tool %s: %w", a.name, err)
			}
			return a.call(ctx, callId, input)
		},
	}
}

func callAnTool(ctx context.Context, callId, name, args string) (anthropic.ContentBlockParamUnion, bool, error) {
	anTool, ok := anToolsByName[name]
	if !ok {
		return anthropic.ContentBlockParamUnion{}, false, nil
	}
	res, err := anTool.call(ctx, callId, args)
	if err != nil {
		return anthropic.ContentBlockParamUnion{}, false, fmt.Errorf("failed to call tool %s: %w", name, err)
	}
	return res, true, nil
}

func toolInputSchemaFor[Input any]() (anthropic.ToolInputSchemaParam, error) {
	bs, err := util.SchemaForInput[Input]()
	if err != nil {
		return anthropic.ToolInputSchemaParam{}, fmt.Errorf("failed to generate json schema: %w", err)
	}

	var inputSchema anthropic.ToolInputSchemaParam
	if err := inputSchema.UnmarshalJSON([]byte(bs)); err != nil {
		return anthropic.ToolInputSchemaParam{}, fmt.Errorf("failed to unmarshal tool input schema: %w", err)
	}
	inputSchema.Type = "" // elided
	return inputSchema, nil
}

type loadImageByUrlInput struct {
	Urls []string `json:"urls"`
}

func loadImagesByUrl() anToolTyped[loadImageByUrlInput] {
	return anToolTyped[loadImageByUrlInput]{
		name:        "load_images_by_url",
		description: "Load images from URL.",
		call: func(ctx context.Context, callId string, input loadImageByUrlInput) (anthropic.ContentBlockParamUnion, error) {
			blocks := make([]anthropic.ToolResultBlockParamContentUnion, len(input.Urls))
			for i, url := range input.Urls {
				blocks[i] = anthropic.ToolResultBlockParamContentUnion{
					OfImage: &anthropic.ImageBlockParam{
						Source: anthropic.ImageBlockParamSourceUnion{
							OfURL: &anthropic.URLImageSourceParam{
								URL: url,
							},
						},
					},
				}
			}
			return anthropic.ContentBlockParamUnion{
				OfToolResult: &anthropic.ToolResultBlockParam{
					ToolUseID: callId,
					Content:   blocks,
				},
			}, nil
		},
	}
}

type loadImagesByFileInput struct {
	Items []loadImagesByFileInputItem `json:"items"`
}

type loadImagesByFileInputItem struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}

func loadImagesByFile() anToolTyped[loadImagesByFileInput] {
	return anToolTyped[loadImagesByFileInput]{
		name:        "load_images_by_file",
		description: "Load images from URL. Supported media_type: [\"image/jpeg\", \"image/png\", \"image/gif\", \"image/webp\"]",
		call: func(ctx context.Context, callId string, input loadImagesByFileInput) (anthropic.ContentBlockParamUnion, error) {
			blocks := make([]anthropic.ToolResultBlockParamContentUnion, len(input.Items))
			for i, item := range input.Items {
				mediaType, err := toMediaType(item.MediaType)
				if err != nil {
					return anthropic.ContentBlockParamUnion{}, fmt.Errorf("invalid media type for file %s: %w", item.Path, err)
				}
				base64Data, err := util.ReadFileAsBase64(item.Path)
				if err != nil {
					return anthropic.ContentBlockParamUnion{}, fmt.Errorf("failed to read file %s: %w", item.Path, err)
				}
				blocks[i] = anthropic.ToolResultBlockParamContentUnion{
					OfImage: &anthropic.ImageBlockParam{
						Source: anthropic.ImageBlockParamSourceUnion{
							OfBase64: &anthropic.Base64ImageSourceParam{
								Data:      base64Data,
								MediaType: mediaType,
							},
						},
					},
				}
			}
			return anthropic.ContentBlockParamUnion{
				OfToolResult: &anthropic.ToolResultBlockParam{
					ToolUseID: callId,
					Content:   blocks,
				},
			}, nil
		},
	}
}

func toMediaType(mediaType string) (anthropic.Base64ImageSourceMediaType, error) {
	switch mediaType {
	case "image/jpeg":
		return anthropic.Base64ImageSourceMediaTypeImageJPEG, nil
	case "image/png":
		return anthropic.Base64ImageSourceMediaTypeImagePNG, nil
	case "image/gif":
		return anthropic.Base64ImageSourceMediaTypeImageGIF, nil
	case "image/webp":
		return anthropic.Base64ImageSourceMediaTypeImageWebP, nil
	default:
		return "", fmt.Errorf("unsupported media type: %s", mediaType)
	}
}
