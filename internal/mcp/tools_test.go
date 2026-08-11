package mcp

import (
	"reflect"
	"testing"
)

func TestFSDeleteInputUsesTokenSpace(t *testing.T) {
	if _, ok := reflect.TypeOf(fsDeleteInput{}).FieldByName("SpaceID"); ok {
		t.Fatal("fs_delete should use the token-bound space, not require space_id")
	}
}

func TestNormalizeSpaceUsesTokenBoundSpace(t *testing.T) {
	tc := &ToolContext{Username: "alice", Role: "user", SpaceID: "47a2efe5"}
	if got := normalizeSpace(tc, ""); got != tc.SpaceID {
		t.Fatalf("normalizeSpace() = %q, want token space %q", got, tc.SpaceID)
	}
}
