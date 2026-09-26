package mangalib

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ErrInvalidSlug is returned when input is neither a slug nor a MangaLib title link.
var ErrInvalidSlug = errors.New("not a mangalib slug or link")

var slugPattern = regexp.MustCompile(`^[0-9]+--[A-Za-z0-9_-]+$`)

// ParseSlug accepts a slug ("7965--chainsaw-man") or a MangaLib link
// ("https://mangalib.me/ru/manga/7965--chainsaw-man?section=chapters") and returns the slug.
func ParseSlug(input string) (string, error) {
	s := strings.TrimSpace(input)
	if slugPattern.MatchString(s) {
		return s, nil
	}
	if strings.Contains(s, "/") {
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		if u, err := url.Parse(s); err == nil {
			for _, seg := range strings.Split(u.Path, "/") {
				if slugPattern.MatchString(seg) {
					return seg, nil
				}
			}
		}
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidSlug, input)
}
