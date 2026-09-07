package tools

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

func TestToolSeqSchemasUseDecimalStrings(t *testing.T) {
	for name, typeName := range map[string]string{
		"handoff seq_s":   handoffInputSchema().Properties["seq_s"].Type,
		"handoff seq_e":   handoffInputSchema().Properties["seq_e"].Type,
		"rewind from_seq": rewindInputSchema().Properties["from_seq"].Type,
	} {
		if typeName != "string" {
			t.Errorf("%s schema type = %q, want string", name, typeName)
		}
	}
}

func TestToolArgsDelegateSequenceValidationToEntrySeq(t *testing.T) {
	for name, target := range map[string]any{
		"handoff": &HandoffArgs{},
		"rewind":  &RewindArgs{},
	} {
		field := "seq_s"
		if name == "rewind" {
			field = "from_seq"
		}
		err := json.Unmarshal([]byte(`{"`+field+`":"01"}`), target)
		if !errors.Is(err, entry.ErrInvalidSeq) {
			t.Errorf("%s invalid sequence error = %v, want ErrInvalidSeq", name, err)
		}
	}
}
