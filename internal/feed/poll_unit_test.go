package feed

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestPollPercent(t *testing.T) {
	for _, c := range []struct {
		voters, votes int
		want          int
	}{
		{0, 0, 0},
		{0, 5, 0},
		{10, 0, 0},
		{3, 1, 33},
		{3, 2, 66},
		{3, 3, 100},
		{5, 5, 100},
		{4, 1, 25},
	} {
		p := Poll{Voters: c.voters}
		if got := p.Percent(PollOption{Votes: c.votes}); got != c.want {
			t.Errorf("Percent(%d voters, %d votes) = %d, want %d", c.voters, c.votes, got, c.want)
		}
	}
}

func TestPollOptions(t *testing.T) {
	for _, c := range []struct {
		name     string
		question string
		options  []string
		want     []string
		errKey   string
	}{
		{
			name:     "valid simple",
			question: "What is your preference?",
			options:  []string{"A", "B"},
			want:     []string{"A", "B"},
		},
		{
			name:     "trim and deduplicate",
			question: "Favorite color?",
			options:  []string{" Red ", "Blue", "Red", " Blue ", "Green"},
			want:     []string{"Red", "Blue", "Green"},
		},
		{
			name:     "skip empty options",
			question: "Choice?",
			options:  []string{"", "  ", "First", "\t", "Second", "   "},
			want:     []string{"First", "Second"},
		},
		{
			name:     "empty question",
			question: "",
			options:  []string{"Yes", "No"},
			errKey:   "err.poll",
		},
		{
			name:     "question too long",
			question: strings.Repeat("ж", 301),
			options:  []string{"Yes", "No"},
			errKey:   "err.poll",
		},
		{
			name:     "question exactly 300 runes",
			question: strings.Repeat("ж", 300),
			options:  []string{"Yes", "No"},
			want:     []string{"Yes", "No"},
		},
		{
			name:     "too few options",
			question: "Only one?",
			options:  []string{"Alone"},
			errKey:   "err.poll",
		},
		{
			name:     "all duplicate options",
			question: "All same?",
			options:  []string{"Yes", " Yes ", "Yes"},
			errKey:   "err.poll",
		},
		{
			name:     "too many options",
			question: "Pick one",
			options: []string{
				"1", "2", "3", "4", "5", "6", "7", "8", "9", "10",
				"11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21",
			},
			errKey: "err.poll",
		},
		{
			name:     "exactly 20 options",
			question: "Pick one",
			options: []string{
				"1", "2", "3", "4", "5", "6", "7", "8", "9", "10",
				"11", "12", "13", "14", "15", "16", "17", "18", "19", "20",
			},
			want: []string{
				"1", "2", "3", "4", "5", "6", "7", "8", "9", "10",
				"11", "12", "13", "14", "15", "16", "17", "18", "19", "20",
			},
		},
		{
			name:     "option too long",
			question: "Long answer?",
			options:  []string{"Short", strings.Repeat("a", 201)},
			errKey:   "err.poll",
		},
		{
			name:     "option exactly 200 runes",
			question: "Long answer?",
			options:  []string{"Short", strings.Repeat("a", 200)},
			want:     []string{"Short", strings.Repeat("a", 200)},
		},
	} {
		got, err := pollOptions(c.question, c.options)
		if c.errKey != "" {
			var ie *i18n.Error
			if !errors.As(err, &ie) || ie.Key != c.errKey {
				t.Errorf("%s: pollOptions() err = %v, want key %q", c.name, err, c.errKey)
			}
		} else {
			if err != nil {
				t.Errorf("%s: pollOptions() unexpected err: %v", c.name, err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("%s: pollOptions() = %v, want %v", c.name, got, c.want)
			}
		}
	}
}

func TestCheckPoll(t *testing.T) {
	if err := CheckPoll("  What?  ", []string{"Yes", "No"}); err != nil {
		t.Errorf("CheckPoll valid returned err: %v", err)
	}
	if err := CheckPoll("   ", []string{"Yes", "No"}); err == nil {
		t.Error("CheckPoll with whitespace question wanted err, got nil")
	}
	if err := CheckPoll("What?", []string{"Only one"}); err == nil {
		t.Error("CheckPoll with single option wanted err, got nil")
	}
}

func TestAddPollValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	err := s.AddPoll(ctx, "author", "post", " ", []string{"A", "B"}, PollKind{})
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.poll" {
		t.Errorf("AddPoll() with whitespace question err = %v, want err.poll", err)
	}

	err = s.AddPoll(ctx, "author", "post", "Question?", []string{"A"}, PollKind{})
	if !errors.As(err, &ie) || ie.Key != "err.poll" {
		t.Errorf("AddPoll() with single option err = %v, want err.poll", err)
	}
}
