package builtin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

func decodeLuaJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("unexpected trailing JSON value")
		}
		return nil, err
	}
	return normalizeLuaJSON(value)
}

func normalizeLuaJSON(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		if integer, err := strconv.ParseInt(string(value), 10, 64); err == nil {
			return integer, nil
		}
		number, err := strconv.ParseFloat(string(value), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid JSON number %q: %w", value, err)
		}
		return number, nil
	case []any:
		for index := range value {
			normalized, err := normalizeLuaJSON(value[index])
			if err != nil {
				return nil, err
			}
			value[index] = normalized
		}
	case map[string]any:
		for key, element := range value {
			normalized, err := normalizeLuaJSON(element)
			if err != nil {
				return nil, err
			}
			value[key] = normalized
		}
	}
	return value, nil
}
