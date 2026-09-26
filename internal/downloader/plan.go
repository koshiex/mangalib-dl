package downloader

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/koshiex/mangalib-dl/internal/mangalib"
)

const (
	minVolumeWidth  = 2
	minChapterWidth = 3
	maxTeamsInLabel = 3
)

// BranchInfo describes a translation branch of a title.
type BranchInfo struct {
	Key      int64  `json:"key"`
	Teams    string `json:"teams"`
	Chapters int    `json:"chapters"`
}

// BranchStats lists branches of the chapters, most complete first.
func BranchStats(chapters []mangalib.Chapter) []BranchInfo {
	index := make(map[int64]int)
	teams := make(map[int64][]string)
	var out []BranchInfo
	for _, ch := range chapters {
		for _, b := range ch.Branches {
			k := b.Key()
			teams[k] = appendUnique(teams[k], branchTeams(b)...)
			if i, ok := index[k]; ok {
				out[i].Chapters++
				continue
			}
			index[k] = len(out)
			out = append(out, BranchInfo{Key: k, Chapters: 1})
		}
	}
	for i := range out {
		out[i].Teams = teamsLabel(teams[out[i].Key])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Chapters > out[j].Chapters })
	return out
}

func branchTeams(b mangalib.Branch) []string {
	names := make([]string, 0, len(b.Teams))
	for _, t := range b.Teams {
		if n := strings.TrimSpace(t.Name); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 && b.User.Username != "" {
		names = append(names, b.User.Username)
	}
	return names
}

func appendUnique(list []string, items ...string) []string {
	for _, it := range items {
		if !slices.Contains(list, it) {
			list = append(list, it)
		}
	}
	return list
}

func teamsLabel(names []string) string {
	switch {
	case len(names) == 0:
		return "без команды"
	case len(names) > maxTeamsInLabel:
		return strings.Join(names[:maxTeamsInLabel], ", ") + fmt.Sprintf(" и ещё %d", len(names)-maxTeamsInLabel)
	default:
		return strings.Join(names, ", ")
	}
}

func resolveBranch(requested int64, chapters []mangalib.Chapter) int64 {
	if requested != BranchAuto {
		return requested
	}
	if stats := BranchStats(chapters); len(stats) > 0 {
		return stats[0].Key
	}
	return 0
}

// branchFor returns the preferred branch when the chapter has it, otherwise the chapter's first branch.
func branchFor(ch mangalib.Chapter, preferred int64) int64 {
	for _, b := range ch.Branches {
		if b.Key() == preferred {
			return preferred
		}
	}
	if len(ch.Branches) > 0 {
		return ch.Branches[0].Key()
	}
	return 0
}

func countFallbacks(chapters []mangalib.Chapter, preferred int64) int {
	n := 0
	for _, ch := range chapters {
		if len(ch.Branches) > 0 && branchFor(ch, preferred) != preferred {
			n++
		}
	}
	return n
}

func selectChapters(all []mangalib.Chapter, req Request) []mangalib.Chapter {
	ids := make(map[int64]bool, len(req.ChapterIDs))
	for _, id := range req.ChapterIDs {
		ids[id] = true
	}
	var out []mangalib.Chapter
	for _, ch := range all {
		if len(ids) > 0 && !ids[ch.ID] {
			continue
		}
		if !inRanges(ch.Number, req.Chapters) || !inRanges(ch.Volume, req.Volumes) {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// entry is a chapter with its names inside an archive.
type entry struct {
	chapter mangalib.Chapter
	label   string
	folder  string
}

// unit is one archive to produce.
type unit struct {
	fileBase string
	volume   string
	number   string
	title    string
	flat     bool
	entries  []entry
}

type numbering struct {
	volWidth, chWidth int
}

func newNumbering(all []mangalib.Chapter) numbering {
	n := numbering{volWidth: minVolumeWidth, chWidth: minChapterWidth}
	for _, ch := range all {
		n.volWidth = max(n.volWidth, intDigits(ch.Volume))
		n.chWidth = max(n.chWidth, intDigits(ch.Number))
	}
	return n
}

func (n numbering) volume(v string) string { return "Vol." + padNumber(v, n.volWidth) }

func (n numbering) chapter(c string) string { return "Ch." + padNumber(c, n.chWidth) }

func (n numbering) label(ch mangalib.Chapter) string {
	return n.volume(ch.Volume) + " " + n.chapter(ch.Number)
}

func intDigits(s string) int {
	whole, _, _ := strings.Cut(strings.TrimSpace(s), ".")
	return len(strings.TrimLeft(whole, "0"))
}

// padNumber zero-pads the integer part so names sort correctly: ("5", 3) -> "005", ("10.5", 3) -> "010.5".
func padNumber(s string, width int) string {
	whole, frac, hasFrac := strings.Cut(strings.TrimSpace(s), ".")
	if whole == "" || strings.Trim(whole, "0123456789") != "" {
		return s
	}
	whole = strings.TrimLeft(whole, "0")
	if len(whole) < width {
		whole = strings.Repeat("0", width-len(whole)) + whole
	}
	if hasFrac {
		return whole + "." + frac
	}
	return whole
}

func planUnits(title string, selected, all []mangalib.Chapter, mode Mode) []unit {
	num := newNumbering(all)
	entries := make([]entry, len(selected))
	for i, ch := range selected {
		label := num.label(ch)
		folder := label
		if name := strings.TrimSpace(ch.Name); name != "" {
			folder += " - " + name
		}
		entries[i] = entry{chapter: ch, label: label, folder: sanitizeName(folder)}
	}
	switch mode {
	case ModeChapter:
		return planChapters(title, entries)
	case ModeSingle:
		base := title
		if len(selected) < len(all) {
			base += " (" + entriesSpan(entries) + ")"
		}
		return []unit{{fileBase: sanitizeName(base), entries: entries}}
	default:
		return planVolumes(title, entries, all, num)
	}
}

func planChapters(title string, entries []entry) []unit {
	units := make([]unit, len(entries))
	for i, e := range entries {
		units[i] = unit{
			fileBase: sanitizeName(title + " - " + e.label),
			volume:   e.chapter.Volume,
			number:   e.chapter.Number,
			title:    strings.TrimSpace(e.chapter.Name),
			flat:     true,
			entries:  []entry{e},
		}
	}
	return units
}

func planVolumes(title string, entries []entry, all []mangalib.Chapter, num numbering) []unit {
	perVolume := make(map[string]int)
	for _, ch := range all {
		perVolume[ch.Volume]++
	}
	index := make(map[string]int)
	var units []unit
	for _, e := range entries {
		v := e.chapter.Volume
		i, ok := index[v]
		if !ok {
			i = len(units)
			index[v] = i
			units = append(units, unit{volume: v})
		}
		units[i].entries = append(units[i].entries, e)
	}
	for i := range units {
		base := title + " - " + num.volume(units[i].volume)
		if len(units[i].entries) < perVolume[units[i].volume] {
			base += " (" + chapterSpan(units[i].entries, num) + ")"
		}
		units[i].fileBase = sanitizeName(base)
	}
	return units
}

func entriesSpan(entries []entry) string {
	first, last := entries[0], entries[len(entries)-1]
	if len(entries) == 1 {
		return first.label
	}
	return first.label + " - " + last.label
}

func chapterSpan(entries []entry, num numbering) string {
	first := num.chapter(entries[0].chapter.Number)
	if len(entries) == 1 {
		return first
	}
	return first + "-" + padNumber(entries[len(entries)-1].chapter.Number, num.chWidth)
}
