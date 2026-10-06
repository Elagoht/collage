package typecheck

import (
	"sort"
	"strings"
)

// suggest returns the candidate closest to name: an exact match ignoring case,
// or the nearest by edit distance (a swap of two letters counting once) within
// one edit for short names and two for names of five letters or more. Ties go
// to the alphabetically first. It returns "" when nothing is that close.
func suggest(name string, candidates []string) string {
	limit := 1
	if len(name) >= 5 {
		limit = 2
	}
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	best, bestDistance := "", limit+1
	for _, c := range sorted {
		if strings.EqualFold(c, name) {
			return c
		}
		if d := distance(strings.ToLower(name), strings.ToLower(c)); d < bestDistance {
			best, bestDistance = c, d
		}
	}
	return best
}

// distance is the optimal string alignment distance between a and b.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range rb {
		d[0][j+1] = j + 1
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}
