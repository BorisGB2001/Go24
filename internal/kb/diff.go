package kb

import (
	"context"
	"strings"
)

// DiffLine is a line of the difference of two texts: Op "=" — in both, "-" — only in the old one, "+" — only in the new.
type DiffLine struct {
	Op, Text string
}

// Diff is the line by line difference of the old text and the new one (the longest common subsequence of lines).
func Diff(old, cur string) []DiffLine {
	a := strings.Split(strings.ReplaceAll(old, "\r\n", "\n"), "\n")
	b := strings.Split(strings.ReplaceAll(cur, "\r\n", "\n"), "\n")
	if len(a)*len(b) > 4_000_000 { // too long to compare: all of the old out, all of the new in
		out := make([]DiffLine, 0, len(a)+len(b))
		for _, l := range a {
			out = append(out, DiffLine{"-", l})
		}
		for _, l := range b {
			out = append(out, DiffLine{"+", l})
		}
		return out
	}
	// lcs[i][j]: the common lines of a[i:] and b[j:]
	lcs := make([][]int32, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out = append(out, DiffLine{"=", a[i]})
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, DiffLine{"-", a[i]})
			i++
		default:
			out = append(out, DiffLine{"+", b[j]})
			j++
		}
	}
	return out
}

// RestoreVersion makes an earlier version of a page its text again; the text it had goes to the history.
func (s *Store) RestoreVersion(ctx context.Context, userID, pageID string, id int64) error {
	old, err := s.Version(ctx, userID, pageID, id)
	if err != nil {
		return err
	}
	return s.SavePage(ctx, userID, pageID, old.Title, old.Body)
}
