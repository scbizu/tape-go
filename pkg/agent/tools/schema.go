package tools

import "github.com/google/jsonschema-go/jsonschema"

func decimalSeqSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Description: description,
	}
}

func handoffInputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"summary": {Type: "string", Description: "Summary for the archived context window."},
			"seq_s":   decimalSeqSchema("First archived entry sequence; zero uses the current tape view."),
			"seq_e":   decimalSeqSchema("Exclusive archived entry sequence; zero uses the next anchor sequence."),
		},
		PropertyOrder: []string{"summary", "seq_s", "seq_e"},
	}
}

func handoffOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"Summary": {Type: "string"},
			"SeqS":    decimalSeqSchema("First archived entry sequence."),
			"SeqE":    decimalSeqSchema("Exclusive archived entry sequence."),
		},
		Required:      []string{"Summary", "SeqS", "SeqE"},
		PropertyOrder: []string{"Summary", "SeqS", "SeqE"},
	}
}

func rewindInputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"from_seq":    decimalSeqSchema("Entry sequence to rewind from; zero means the latest entry."),
			"max_anchors": {Type: "integer", Description: "Maximum anchors to rewind; zero defaults to one."},
		},
		PropertyOrder: []string{"from_seq", "max_anchors"},
	}
}

func rewindOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"SeqS": decimalSeqSchema("First entry sequence."),
			"SeqE": decimalSeqSchema("Exclusive entry sequence."),
		},
		Required:      []string{"SeqS", "SeqE"},
		PropertyOrder: []string{"SeqS", "SeqE"},
	}
}
