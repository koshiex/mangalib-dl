package mangalib

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TypeManga is the API type id of Japanese manga (read right to left).
const TypeManga = 1

// Cover holds cover image URLs of a title.
type Cover struct {
	Thumbnail string `json:"thumbnail"`
	Default   string `json:"default"`
}

// Label is an id/label pair the API uses for enums (type, status, age rating).
type Label struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// MangaSummary is a title as returned by the search endpoint.
type MangaSummary struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	RusName           string `json:"rus_name"`
	EngName           string `json:"eng_name"`
	SlugURL           string `json:"slug_url"`
	Cover             Cover  `json:"cover"`
	AgeRestriction    Label  `json:"ageRestriction"`
	Type              Label  `json:"type"`
	Status            Label  `json:"status"`
	IsLicensed        bool   `json:"is_licensed"`
	ReleaseDateString string `json:"releaseDateString"`
}

// DisplayName returns the Russian title when present, otherwise the original one.
func (m MangaSummary) DisplayName() string {
	if m.RusName != "" {
		return m.RusName
	}
	if m.Name != "" {
		return m.Name
	}
	return m.EngName
}

// Person is an author or an artist.
type Person struct {
	Name    string `json:"name"`
	RusName string `json:"rus_name"`
}

// Genre is a title genre.
type Genre struct {
	Name string `json:"name"`
}

// RichText is a field the API returns either as a plain string or as a ProseMirror document.
type RichText string

// UnmarshalJSON accepts a string or a document and keeps its text with paragraph breaks.
func (t *RichText) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*t = RichText(s)
		return nil
	}
	var doc richNode
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("rich text: %w", err)
	}
	var b strings.Builder
	doc.render(&b)
	*t = RichText(strings.TrimSpace(b.String()))
	return nil
}

type richNode struct {
	Type    string     `json:"type"`
	Text    string     `json:"text"`
	Content []richNode `json:"content"`
}

func (n richNode) render(b *strings.Builder) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
		return
	case "hardBreak":
		b.WriteString("\n")
		return
	}
	for _, c := range n.Content {
		c.render(b)
	}
	if n.Type == "paragraph" || n.Type == "heading" {
		b.WriteString("\n")
	}
}

// Manga is a detailed title description.
type Manga struct {
	MangaSummary
	OtherNames []string `json:"otherNames"`
	Summary    RichText `json:"summary"`
	Authors    []Person `json:"authors"`
	Artists    []Person `json:"artists"`
	Genres     []Genre  `json:"genres"`
}

// Team is a translation team.
type Team struct {
	Name string `json:"name"`
}

// Uploader is the user who uploaded a chapter.
type Uploader struct {
	Username string `json:"username"`
}

// Branch is one translation of a chapter.
type Branch struct {
	ID       int64    `json:"id"`
	BranchID *int64   `json:"branch_id"`
	Teams    []Team   `json:"teams"`
	User     Uploader `json:"user"`
}

// Key returns the branch id, or 0 for the default (unnamed) branch.
func (b Branch) Key() int64 {
	if b.BranchID == nil {
		return 0
	}
	return *b.BranchID
}

// Chapter is an entry of the chapter list.
type Chapter struct {
	ID       int64    `json:"id"`
	Volume   string   `json:"volume"`
	Number   string   `json:"number"`
	Name     string   `json:"name"`
	Branches []Branch `json:"branches"`
}

// Page is a single chapter page. URL is relative to an image server.
type Page struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ChapterPages is a chapter with its pages in reading order.
type ChapterPages struct {
	ID     int64  `json:"id"`
	Volume string `json:"volume"`
	Number string `json:"number"`
	Name   string `json:"name"`
	Pages  []Page `json:"pages"`
}

// ImageServer is a CDN host for chapter pages.
type ImageServer struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	SiteIDs []int  `json:"site_ids"`
}

// SearchPage is one page of search results.
type SearchPage struct {
	Items   []MangaSummary
	HasNext bool
}
