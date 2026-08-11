package mcp

import (
	"reflect"
	"testing"
)

func TestFSDeleteInputExposesSpaceID(t *testing.T) {
	field, ok := reflect.TypeOf(fsDeleteInput{}).FieldByName("SpaceID")
	if !ok {
		t.Fatal("fs_delete input has no space_id field")
	}
	if got := field.Tag.Get("json"); got != "space_id,omitempty" {
		t.Fatalf("space_id json tag = %q", got)
	}
}
