package openai

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"mime"
	"os"
	"path/filepath"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/util"
)

// CallTool implements [llm.ToolCaller].
func (c *Client) CallTool(ctx context.Context, toolName, args string) (string, bool, error) {
	_, ok := oaiToolsByName[toolName]
	if !ok {
		return "", false, nil
	}
	return consts.LlmInternalToolHandling, true, nil
}

type Nothing struct{}

var (
	oaiTools = []oaiTool{
		loadLocalFile(),
		// oaiToolTyped[Nothing, []openai.Model]{
		// 	name:        "list_llm_models",
		// 	description: "list all llm models supported by current openai host",
		// 	call: func(ctx context.Context, c *Client, input *Nothing) (*[]openai.Model, error) {
		// 		res, err := c.listModels(ctx)
		// 		return &res, err
		// 	},
		// }.compile(),
	}
	oaiToolsByName = func() map[string]oaiTool {
		m := make(map[string]oaiTool, len(oaiTools))
		for _, tool := range oaiTools {
			m[tool.name] = tool
		}
		return m
	}()
	oaiApiTools = func() []responses.ToolUnionParam {
		res := make([]responses.ToolUnionParam, len(oaiTools))
		for i := range oaiTools {
			res[i] = responses.ToolUnionParam{
				OfFunction: &responses.FunctionToolParam{
					Parameters:  oaiTools[i].schema,
					Name:        oaiTools[i].name,
					Description: param.NewOpt(oaiTools[i].description),
				},
			}
		}
		return res
	}()
)

type oaiToolTyped[Input any] struct {
	name        string
	description string
	call        func(ctx context.Context, input *Input) (responses.ResponseInputItemUnionParam, error)
}

type oaiTool struct {
	name        string
	description string
	schema      map[string]any
	call        func(ctx context.Context, args string) (responses.ResponseInputItemUnionParam, error)
}

func (a oaiToolTyped[Input]) compile() oaiTool {
	schema, err := toolInputSchemaFor[Input]()
	if err != nil {
		panic(fmt.Sprintf("failed to generate tool input schema for tool %s: %v", a.name, err))
	}
	return oaiTool{
		name:        a.name,
		description: a.description,
		schema:      schema,
		call: func(ctx context.Context, args string) (responses.ResponseInputItemUnionParam, error) {
			var input Input
			if err := json.Unmarshal([]byte(args), &input); err != nil {
				return responses.ResponseInputItemUnionParam{}, fmt.Errorf("failed to unmarshal tool input for tool %s: %w", a.name, err)
			}
			out, err := a.call(ctx, &input)
			if err != nil {
				return responses.ResponseInputItemUnionParam{}, err
			}
			return out, nil
		},
	}
}

func toolInputSchemaFor[Input any]() (map[string]any, error) {
	bs, err := util.SchemaForInput[Input]()
	if err != nil {
		return nil, fmt.Errorf("failed to generate json schema: %w", err)
	}
	var inputSchema map[string]any
	if err := json.Unmarshal(bs, &inputSchema); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tool input schema: %w", err)
	}
	return inputSchema, nil
}

func (c *Client) listModels(ctx context.Context) ([]openai.Model, error) {
	res := []openai.Model{}

	modelPages, err := c.client.Models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("c.client.Models.List(ctx) -> %w", err)
	}
	res = append(res, modelPages.Data...)

	for {
		nextPage, err := modelPages.GetNextPage()
		if err != nil {
			return nil, fmt.Errorf("modelPages.GetNextPage() -> %w", err)
		}
		if nextPage == nil {
			break
		}
		res = append(res, nextPage.Data...)
		modelPages = nextPage
	}

	return res, nil
}

func loadLocalFile() oaiTool {
	type LoadLocalFilesInput struct {
		Paths []string `json:"paths"`
	}
	return oaiToolTyped[LoadLocalFilesInput]{
		name:        "load_local_files",
		description: "given list of file paths, loads the local files into llm context window. only use for non-text files, such as images and pdf",
		call: func(ctx context.Context, input *LoadLocalFilesInput) (responses.ResponseInputItemUnionParam, error) {
			var contents responses.ResponseInputMessageContentListParam
			for _, p := range input.Paths {
				url, err := localFileBase64EncodeUrl(p)
				if err != nil {
					return responses.ResponseInputItemUnionParam{}, fmt.Errorf("failed to encode local file %s: %w", p, err)
				}
				contents = append(contents, responses.ResponseInputContentUnionParam{
					OfInputFile: &responses.ResponseInputFileParam{
						FileURL: param.NewOpt(url),
					},
				})
			}

			return responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role: responses.EasyInputMessageRoleUser,
					Content: responses.EasyInputMessageContentUnionParam{
						OfInputItemContentList: contents,
					},
				},
			}, nil
		},
	}.compile()
}

func localFileBase64EncodeUrl(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", path, err)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(path))
	if mimeType == "" {
		return "", fmt.Errorf("failed to determine mime type for file %s", path)
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
