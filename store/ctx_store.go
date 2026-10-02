package store

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"encoding/json/v2"

	"github.com/speed2exe/ag/models"
)

type CtxStore struct{ dir string }

func NewCtxStore(dir string) *CtxStore {
	return &CtxStore{dir: dir}
}

func (s *CtxStore) Save(contextId string, messages []models.Message) error {
	// make sure the directory exists
	dirPath := fmt.Sprintf("%s/%s", s.dir, contextId)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// calculate how many 0 padding we need
	nDigits := 0
	n := len(messages)
	for n > 0 {
		n /= 10
		nDigits++
	}

	for i, msg := range messages {
		paddedIndex := pad(i, nDigits)
		var suffix string
		switch msg.(type) {
		case *models.Summary:
			suffix = "summary"
		case *models.ContextId:
			suffix = "context"
		case *models.UserPrompt:
			suffix = "user"
		case *models.AgentReasoning:
			suffix = "agent_reasoning"
		case *models.AgentResponse:
			suffix = "agent_response"
		case *models.ToolCalls:
			suffix = "tools"
		case *models.OutputErr:
			suffix = "error"
		default:
			panic(fmt.Sprintf("unsupported message type: %T", msg))
		}
		fileName := fmt.Sprintf("%s-%s.json", paddedIndex, suffix)
		if err := s.writeToDiskJson(contextId, fileName, msg); err != nil {
			return err
		}
	}

	return nil
}

func (s *CtxStore) writeToDiskJson(contextId string, fileName string, a any) error {
	f, err := os.Create(fmt.Sprintf("%s/%s/%s", s.dir, contextId, fileName))
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	if err := json.MarshalWrite(f, a); err != nil {
		return fmt.Errorf("failed to encode json: %w", err)
	}
	return nil
}

func (s *CtxStore) Load(contextId string) ([]models.Message, error) {
	dirPath := fmt.Sprintf("%s/%s", s.dir, contextId)
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	// sort files lexicographically to ensure correct order of messages
	// since file names are prefixed with zero-padded index, lexicographical order is the same as chronological order
	// e.g. 001-user.json < 002-agent.json < 003-tool.json
	// if we have more than 10 messages, we need to pad with zeros to ensure correct order
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	messages := make([]models.Message, 0, len(files))
	for _, file := range files {
		// we need to determine the type of message based on the file name suffix
		fileName := file.Name()
		parts := strings.Split(fileName, "-")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid file name: %s", fileName)
		}
		parts = strings.Split(parts[1], ".")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid file name: %s", fileName)
		}
		suffix := parts[0]

		bs, err := os.ReadFile(fmt.Sprintf("%s/%s/%s", s.dir, contextId, file.Name()))
		if err != nil {
			return nil, fmt.Errorf("failed to read file: %w", err)
		}

		switch suffix {
		case "summary":
			var summaryMsg models.Summary
			if err := json.Unmarshal(bs, &summaryMsg); err != nil {
				return nil, fmt.Errorf("failed to unmarshal summary message: %w", err)
			}
			messages = append(messages, &summaryMsg)
		case "context":
			var contextMsg models.ContextId
			if err := json.Unmarshal(bs, &contextMsg); err != nil {
				return nil, fmt.Errorf("failed to unmarshal context message: %w", err)
			}
			prevCtxMessages, err := s.Load(contextMsg.Id) // recursively load previous context
			if err != nil {
				return nil, fmt.Errorf("failed to load previous context: %w", err)
			}
			messages = append(prevCtxMessages, messages...) // prepend previous context messages

		case "user":
			var userMsg models.UserPrompt
			if err := json.Unmarshal(bs, &userMsg); err != nil {
				return nil, fmt.Errorf("failed to unmarshal user message: %w", err)
			}
			messages = append(messages, &userMsg)
		case "agent_reasoning":
			var agentReasoning models.AgentReasoning
			if err := json.Unmarshal(bs, &agentReasoning); err != nil {
				return nil, fmt.Errorf("failed to unmarshal agent message: %w", err)
			}
			messages = append(messages, &agentReasoning)
		case "agent_response":
			var agentResponse models.AgentResponse
			if err := json.Unmarshal(bs, &agentResponse); err != nil {
				return nil, fmt.Errorf("failed to unmarshal agent message: %w", err)
			}
			messages = append(messages, &agentResponse)
		case "tools":
			var toolCalls models.ToolCalls
			if err := json.Unmarshal(bs, &toolCalls); err != nil {
				return nil, fmt.Errorf("failed to unmarshal tool message: %w", err)
			}
			messages = append(messages, &toolCalls)
		case "error":
			var errorMsg models.OutputErr
			if err := json.Unmarshal(bs, &errorMsg); err != nil {
				return nil, fmt.Errorf("failed to unmarshal error message: %w", err)
			}
			messages = append(messages, &errorMsg)
		default:
			panic(fmt.Sprintf("unsupported file suffix: %s", suffix))
		}
	}
	return messages, nil
}

func pad(num, width int) string {
	str := fmt.Sprintf("%d", num)
	for len(str) < width {
		str = "0" + str
	}
	return str
}
