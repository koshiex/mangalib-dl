package downloader

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/koshiex/mangalib-dl/internal/mangalib"
)

func branch(id int64, teams ...string) mangalib.Branch {
	b := mangalib.Branch{}
	if id > 0 {
		b.BranchID = &id
	}
	for _, t := range teams {
		b.Teams = append(b.Teams, mangalib.Team{Name: t})
	}
	return b
}

func chapter(id int64, vol, num string, branches ...mangalib.Branch) mangalib.Chapter {
	return mangalib.Chapter{ID: id, Volume: vol, Number: num, Branches: branches}
}

func TestParseRanges(t *testing.T) {
	inf := math.Inf(1)
	tests := []struct {
		spec    string
		want    []Range
		wantErr bool
	}{
		{spec: "", want: nil},
		{spec: "5", want: []Range{{5, 5}}},
		{spec: "1-10, 12", want: []Range{{1, 10}, {12, 12}}},
		{spec: "10.5-", want: []Range{{10.5, inf}}},
		{spec: "-3", want: []Range{{math.Inf(-1), 3}}},
		{spec: "1-10,,", want: []Range{{1, 10}}},
		{spec: ",", wantErr: true},
		{spec: "-", wantErr: true},
		{spec: "10-1", wantErr: true},
		{spec: "a-b", wantErr: true},
		{spec: "NaN", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			got, err := ParseRanges(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInRanges(t *testing.T) {
	ranges := []Range{{1, 10}, {20.5, 20.5}}
	tests := []struct {
		value string
		want  bool
	}{
		{"1", true}, {"10", true}, {"5.5", true}, {"11", false}, {"20.5", true}, {"x", false},
	}
	for _, tt := range tests {
		if got := inRanges(tt.value, ranges); got != tt.want {
			t.Errorf("inRanges(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
	if !inRanges("x", nil) {
		t.Error("empty ranges must match everything")
	}
}

func TestSelectChapters(t *testing.T) {
	all := []mangalib.Chapter{
		chapter(1, "1", "1"), chapter(2, "1", "2"), chapter(3, "2", "3"), chapter(4, "2", "4"),
	}
	tests := []struct {
		name string
		req  Request
		want []int64
	}{
		{"all", Request{}, []int64{1, 2, 3, 4}},
		{"by ids", Request{ChapterIDs: []int64{4, 2}}, []int64{2, 4}},
		{"by chapter range", Request{Chapters: []Range{{2, 3}}}, []int64{2, 3}},
		{"by volume", Request{Volumes: []Range{{2, 2}}}, []int64{3, 4}},
		{"ids and volume", Request{ChapterIDs: []int64{1, 3}, Volumes: []Range{{2, 2}}}, []int64{3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []int64
			for _, ch := range selectChapters(all, tt.req) {
				got = append(got, ch.ID)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBranchStatsAndResolve(t *testing.T) {
	chapters := []mangalib.Chapter{
		chapter(1, "1", "1", branch(0, "Alpha"), branch(7, "Beta")),
		chapter(2, "1", "2", branch(7, "Beta"), branch(0, "Gamma")),
		chapter(3, "1", "3", branch(7, "Beta", "Delta")),
		chapter(4, "1", "4"),
	}
	stats := BranchStats(chapters)
	want := []BranchInfo{
		{Key: 7, Teams: "Beta, Delta", Chapters: 3},
		{Key: 0, Teams: "Alpha, Gamma", Chapters: 2},
	}
	if !slices.Equal(stats, want) {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
	if got := resolveBranch(BranchAuto, chapters); got != 7 {
		t.Errorf("auto branch = %d, want 7", got)
	}
	if got := resolveBranch(0, chapters); got != 0 {
		t.Errorf("explicit branch = %d, want 0", got)
	}
	if got := branchFor(chapters[0], 7); got != 7 {
		t.Errorf("branchFor preferred = %d", got)
	}
	if got := branchFor(chapters[2], 0); got != 7 {
		t.Errorf("branchFor fallback = %d, want 7", got)
	}
	if got := branchFor(chapters[3], 7); got != 0 {
		t.Errorf("branchFor no branches = %d, want 0", got)
	}
	if got := countFallbacks(chapters, 0); got != 1 {
		t.Errorf("fallbacks = %d, want 1", got)
	}
}

func TestTeamsLabel(t *testing.T) {
	if got := teamsLabel(nil); got != "без команды" {
		t.Errorf("empty = %q", got)
	}
	if got := teamsLabel([]string{"a", "b", "c", "d", "e"}); got != "a, b, c и ещё 2" {
		t.Errorf("long = %q", got)
	}
}

func TestPadNumber(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"5", 3, "005"},
		{"10.5", 3, "010.5"},
		{"1234", 3, "1234"},
		{"0", 2, "00"},
		{"007", 2, "07"},
		{"extra", 3, "extra"},
		{"", 3, ""},
	}
	for _, tt := range tests {
		if got := padNumber(tt.in, tt.width); got != tt.want {
			t.Errorf("padNumber(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}

func unitNames(units []unit) []string {
	names := make([]string, len(units))
	for i, u := range units {
		names[i] = u.fileBase
	}
	return names
}

func TestPlanUnits(t *testing.T) {
	all := []mangalib.Chapter{
		chapter(1, "1", "1"), chapter(2, "1", "2"), chapter(3, "2", "3"), chapter(4, "2", "3.5"),
	}
	all[0].Name = "Начало"
	tests := []struct {
		name     string
		selected []mangalib.Chapter
		mode     Mode
		want     []string
	}{
		{"volumes full", all, ModeVolume, []string{"T - Vol.01", "T - Vol.02"}},
		{"volume partial", all[1:3], ModeVolume, []string{"T - Vol.01 (Ch.002)", "T - Vol.02 (Ch.003)"}},
		{"chapters", all[2:], ModeChapter, []string{"T - Vol.02 Ch.003", "T - Vol.02 Ch.003.5"}},
		{"single full", all, ModeSingle, []string{"T"}},
		{"single partial", all[:2], ModeSingle, []string{"T (Vol.01 Ch.001 - Vol.01 Ch.002)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unitNames(planUnits("T", tt.selected, all, tt.mode))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}

	units := planUnits("T", all, all, ModeVolume)
	if f := units[0].entries[0].folder; f != "Vol.01 Ch.001 - Начало" {
		t.Errorf("folder = %q", f)
	}
	if units[0].flat {
		t.Error("volume archive must use chapter folders")
	}
	if u := planUnits("T", all[:1], all, ModeChapter)[0]; !u.flat || u.title != "Начало" || u.number != "1" {
		t.Errorf("chapter unit = %+v", u)
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Re:Zero", "Re -Zero"},
		{`a/b\c*d?e"f<g>h|i`, "a_b_c_de'f(g)h_i"},
		{"  много   пробелов \t", "много пробелов"},
		{"...", fallbackName},
		{"line\nbreak", "linebreak"},
	}
	for _, tt := range tests {
		if got := sanitizeName(tt.in); got != tt.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	long := sanitizeName(strings.Repeat("я", 200))
	if len(long) > maxNameBytes || !strings.HasPrefix(long, "я") || strings.ContainsRune(long, '�') {
		t.Errorf("long name not truncated on a rune boundary: %d bytes", len(long))
	}
}
