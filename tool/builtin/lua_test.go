package builtin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestLuaReturnsAllValues(t *testing.T) {
	luaTool := Lua()
	output, err := luaTool.Call(context.Background(), []byte(`{"name":"multi-return","script":"return 1, 'two', true"}`))
	if err != nil {
		t.Fatalf("Lua call failed: %v", err)
	}

	var result struct {
		Result any `json:"result"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("failed to decode Lua output: %v", err)
	}
	expected := []any{float64(1), "two", true}
	if !reflect.DeepEqual(result.Result, expected) {
		t.Fatalf("unexpected result: got %#v, want %#v", result.Result, expected)
	}
}

func TestLuaSingleReturnRemainsScalar(t *testing.T) {
	luaTool := Lua()
	output, err := luaTool.Call(context.Background(), []byte(`{"name":"single-return","script":"return 'value'"}`))
	if err != nil {
		t.Fatalf("Lua call failed: %v", err)
	}

	var result struct {
		Result any `json:"result"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("failed to decode Lua output: %v", err)
	}
	if result.Result != "value" {
		t.Fatalf("unexpected result: got %#v, want %q", result.Result, "value")
	}
}
