package bus

import "strings"

// inlineLimit is the most characters of a contribution a signal carries, the same limit the agent
// runtime puts on an agent's statement. The signal holds the whole statement; nothing is moved to a
// file and replaced by a marker.
const inlineLimit = 24_000

// truncate cuts s to at most n characters (bytes, on a rune boundary).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
