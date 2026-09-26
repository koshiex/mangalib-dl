package mangalib

import (
	"encoding/json"
	"testing"
)

func TestRichTextUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"string", `"plain text"`, "plain text"},
		{"null", `null`, ""},
		{"document", `{"type":"doc","content":[
			{"type":"paragraph","content":[{"type":"text","text":"First"},{"type":"hardBreak"},{"type":"text","text":"line"}]},
			{"type":"paragraph","content":[{"type":"text","text":"Second"}]}]}`, "First\nline\nSecond"},
		{"empty document", `{"type":"doc"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m struct {
				Summary RichText `json:"summary"`
			}
			if err := json.Unmarshal([]byte(`{"summary":`+tt.in+`}`), &m); err != nil {
				t.Fatal(err)
			}
			if string(m.Summary) != tt.want {
				t.Fatalf("got %q, want %q", m.Summary, tt.want)
			}
		})
	}

	var bad RichText
	if err := json.Unmarshal([]byte(`[1,2]`), &bad); err == nil {
		t.Fatal("expected error for array")
	}
}
