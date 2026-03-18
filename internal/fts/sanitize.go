package fts

import "strings"

// SanitizeQuery removes characters that are special in FTS5 syntax.
func SanitizeQuery(q string) string {
	replacer := strings.NewReplacer(
		"?", "", "!", "", ".", "", ",", "", ";", "",
		":", "", "'", "", "\"", "", "(", "", ")", "",
		"*", "", "+", "", "-", "", "^", "",
		"{", "", "}", "", "[", "", "]", "",
		"/", "", "~", "", "@", "", "#", "",
		"&", "", "|", "", "<", "", ">", "", "\\", "", "=", "",
	)
	cleaned := replacer.Replace(q)
	words := strings.Fields(cleaned)
	if len(words) == 0 {
		return ""
	}
	return strings.Join(words, " ")
}
