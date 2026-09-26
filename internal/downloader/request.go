// Package downloader turns a MangaLib title into zip/cbz archives of page images.
package downloader

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Mode controls how chapters are split into archives.
type Mode string

const (
	ModeVolume  Mode = "volume"
	ModeChapter Mode = "chapter"
	ModeSingle  Mode = "single"
)

// ParseMode parses a mode name; empty input means ModeVolume.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return ModeVolume, nil
	case ModeVolume, ModeChapter, ModeSingle:
		return m, nil
	default:
		return "", fmt.Errorf("unknown mode %q (want volume, chapter or single)", s)
	}
}

// Format is the archive file extension. Both are plain zip files.
type Format string

const (
	FormatZip Format = "zip"
	FormatCBZ Format = "cbz"
)

// ParseFormat parses a format name; empty input means FormatZip.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(strings.TrimSpace(s))); f {
	case "":
		return FormatZip, nil
	case FormatZip, FormatCBZ:
		return f, nil
	default:
		return "", fmt.Errorf("unknown format %q (want zip or cbz)", s)
	}
}

// BranchAuto picks the translation branch that covers most chapters.
const BranchAuto int64 = -1

// Request describes what to download.
type Request struct {
	Slug string
	// ChapterIDs limits the download to these chapters; empty means all chapters.
	ChapterIDs []int64
	// Chapters and Volumes filter by chapter and volume number; empty means no filter.
	Chapters []Range
	Volumes  []Range
	// Branch is BranchAuto, 0 for the default branch, or a branch id.
	// Chapters missing in the branch are taken from another one.
	Branch int64
	Mode   Mode
	Format Format
}

// Range is an inclusive numeric interval. Open ends are infinite.
type Range struct {
	Lo, Hi float64
}

// ParseRanges parses specs like "1-10,12,20.5,30-". Empty spec means no filter.
func ParseRanges(spec string) ([]Range, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []Range
	for part := range strings.SplitSeq(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		r, err := parseRange(part)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty range spec %q", spec)
	}
	return out, nil
}

func parseRange(part string) (Range, error) {
	lo, hi, isInterval := strings.Cut(part, "-")
	if !isInterval {
		v, err := parseNumber(part)
		return Range{Lo: v, Hi: v}, err
	}
	lo, hi = strings.TrimSpace(lo), strings.TrimSpace(hi)
	if lo == "" && hi == "" {
		return Range{}, fmt.Errorf("invalid range %q", part)
	}
	r := Range{Lo: math.Inf(-1), Hi: math.Inf(1)}
	var err error
	if lo != "" {
		if r.Lo, err = parseNumber(lo); err != nil {
			return Range{}, err
		}
	}
	if hi != "" {
		if r.Hi, err = parseNumber(hi); err != nil {
			return Range{}, err
		}
	}
	if r.Lo > r.Hi {
		return Range{}, fmt.Errorf("invalid range %q: start is greater than end", part)
	}
	return r, nil
}

func parseNumber(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	return v, nil
}

func inRanges(value string, ranges []Range) bool {
	if len(ranges) == 0 {
		return true
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return false
	}
	for _, r := range ranges {
		if v >= r.Lo && v <= r.Hi {
			return true
		}
	}
	return false
}
