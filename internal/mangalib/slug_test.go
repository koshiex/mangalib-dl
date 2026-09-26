package mangalib

import (
	"errors"
	"testing"
)

func TestParseSlug(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "7965--chainsaw-man", want: "7965--chainsaw-man"},
		{in: "  57751--hwasangwihwan-  ", want: "57751--hwasangwihwan-"},
		{in: "https://mangalib.me/ru/manga/7965--chainsaw-man", want: "7965--chainsaw-man"},
		{in: "https://mangalib.me/ru/manga/7965--chainsaw-man?section=chapters", want: "7965--chainsaw-man"},
		{in: "mangalib.me/ru/7965--chainsaw-man/read/v1/c1", want: "7965--chainsaw-man"},
		{in: "chainsaw man", wantErr: true},
		{in: "chainsaw-man", wantErr: true},
		{in: "7965--chainsaw/../../etc", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseSlug(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidSlug) {
					t.Fatalf("ParseSlug(%q) err = %v, want ErrInvalidSlug", tt.in, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ParseSlug(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}
