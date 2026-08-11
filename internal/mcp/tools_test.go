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
