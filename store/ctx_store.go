package store

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/speed2exe/ag/models"
)

type CtxStore struct{ dir string }

func NewCtxStore(dir string) *CtxStore {
	return &CtxStore{dir: dir}
}

func (s *CtxStore) Save(contextId string, messages []models.Message) error {
	dirPath := filepath.Join(s.dir, contextId)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	width := len(strconv.Itoa(len(messages)))
	for i, msg := range messages {
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
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to encode json: %w", err)
		}
		fileName := fmt.Sprintf("%0*d-%s.json", width, i, suffix)
		if err := os.WriteFile(filepath.Join(dirPath, fileName), data, 0o644); err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}
	}

	return nil
}

func (s *CtxStore) Load(contextId string) ([]models.Message, error) {
	dirPath := filepath.Join(s.dir, contextId)
	// os.ReadDir returns entries sorted by filename; zero-padded numeric
	// prefixes keep lexicographical order identical to chronological order.
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	messages := make([]models.Message, 0, len(files))
	for _, file := range files {
		fileName := file.Name()
		_, rest, hasDash := strings.Cut(fileName, "-")
		suffix, hasExt := strings.CutSuffix(rest, ".json")
		if !hasDash || !hasExt {
			return nil, fmt.Errorf("invalid file name: %s", fileName)
		}

		bs, err := os.ReadFile(filepath.Join(dirPath, fileName))
		if err != nil {
			return nil, fmt.Errorf("failed to read file: %w", err)
		}

		if suffix == "context" {
			var contextMsg models.ContextId
			if err := json.Unmarshal(bs, &contextMsg); err != nil {
				return nil, fmt.Errorf("failed to unmarshal context message: %w", err)
			}
			prevCtxMessages, err := s.Load(contextMsg.Id)
			if err != nil {
				return nil, fmt.Errorf("failed to load previous context: %w", err)
			}
			messages = append(prevCtxMessages, messages...)
			continue
		}

		var msg models.Message
		switch suffix {
		case "summary":
			msg = &models.Summary{}
		case "user":
			msg = &models.UserPrompt{}
		case "agent_reasoning":
			msg = &models.AgentReasoning{}
		case "agent_response":
			msg = &models.AgentResponse{}
		case "tools":
			msg = &models.ToolCalls{}
		case "error":
			msg = &models.OutputErr{}
		default:
			panic(fmt.Sprintf("unsupported file suffix: %s", suffix))
		}
		if err := json.Unmarshal(bs, msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal %s message: %w", suffix, err)
		}
		messages = append(messages, msg)
	}
	return messages, nil
}
