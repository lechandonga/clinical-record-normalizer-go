package ingest

import "strings"

// patch returns body with the first occurrence of old removed/replaced.
func patch(body, old, new string) string {
	return strings.Replace(body, old, new, 1)
}
