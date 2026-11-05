package openlines

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestVoteMark(t *testing.T) {
	cases := []struct {
		vote int
		want string
	}{
		{1, "👍"},
		{2, "👍"},
		{100, "👍"},
		{0, "👎"},
		{-1, "👎"},
		{-2, "👎"},
	}
	for _, tc := range cases {
		if got := voteMark(tc.vote); got != tc.want {
			t.Errorf("voteMark(%d) = %q; want %q", tc.vote, got, tc.want)
		}
	}
}

func TestCleanVote(t *testing.T) {
	t.Run("visitor vote valid values", func(t *testing.T) {
		for _, v := range []int{1, -1} {
			cmt, err := cleanVote(v, "  Great help!  ", false)
			if err != nil || cmt != "Great help!" {
				t.Errorf("cleanVote(%d) = (%q, %v); want ('Great help!', nil)", v, cmt, err)
			}
		}
	})

	t.Run("visitor vote rejects zero and other integers", func(t *testing.T) {
		for _, v := range []int{0, 2, -2, 5} {
			_, err := cleanVote(v, "comment", false)
			if err == nil {
				t.Errorf("cleanVote(%d, head=false) expected error, got nil", v)
			}
			var ve *i18n.Error
			if !errors.As(err, &ve) {
				t.Errorf("expected i18n.Error for vote=%d, got %T (%v)", v, err, err)
			}
		}
	})

	t.Run("head vote accepts zero, 1 and -1", func(t *testing.T) {
		for _, v := range []int{0, 1, -1} {
			cmt, err := cleanVote(v, "  Supervisory note  ", true)
			if err != nil || cmt != "Supervisory note" {
				t.Errorf("cleanVote(%d, head=true) = (%q, %v); want ('Supervisory note', nil)", v, cmt, err)
			}
		}
	})

	t.Run("head vote rejects out of range numbers", func(t *testing.T) {
		for _, v := range []int{2, -2, 10} {
			_, err := cleanVote(v, "", true)
			if err == nil {
				t.Errorf("cleanVote(%d, head=true) expected error, got nil", v)
			}
		}
	})

	t.Run("comment length limit 1000 runes", func(t *testing.T) {
		exact := strings.Repeat("о", 1000)
		cmt, err := cleanVote(1, exact, false)
		if err != nil || cmt != exact {
			t.Errorf("cleanVote with 1000 runes: err=%v", err)
		}

		tooLong := strings.Repeat("о", 1001)
		_, err = cleanVote(1, tooLong, false)
		if err == nil {
			t.Error("expected error for comment > 1000 runes")
		}
	})
}
