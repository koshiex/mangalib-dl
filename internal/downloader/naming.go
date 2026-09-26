package downloader

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxNameBytes keeps file names well under the 255-byte limit of common file systems.
const maxNameBytes = 180

const fallbackName = "untitled"

var unsafeChars = strings.NewReplacer(
	"/", "_", "\\", "_", ":", " -", "*", "_", "?", "", "\"", "'", "<", "(", ">", ")", "|", "_",
)

// sanitizeName makes s safe to use as a file or folder name on any OS.
func sanitizeName(s string) string {
	s = unsafeChars.Replace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(truncateBytes(strings.Trim(s, " ."), maxNameBytes), " .")
	if s == "" {
		return fallbackName
	}
	return s
}

func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
