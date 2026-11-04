package feed

import (
	"testing"
)

func TestReactionOfModifiers(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
	}{
		// Standard reactions
		{"👍", "like"},
		{"😍", "kiss"},
		{"😂", "laugh"},
		{"😮", "wonder"},
		{"😢", "cry"},
		{"😡", "angry"},
		{"🤦", "facepalm"},

		// Fitzpatrick skin tone modifiers
		{"👍🏻", "like"},
		{"👍🏼", "like"},
		{"👍🏽", "like"},
		{"👍🏾", "like"},
		{"👍🏿", "like"},

		// Gender and variation selectors
		{"🤦‍♂️", "facepalm"},  // man facepalming
		{"🤦‍♀️", "facepalm"},  // woman facepalming
		{"🤦🏽‍♂️", "facepalm"}, // man facepalming with skin tone
		{"🤦🏿‍♀️", "facepalm"}, // woman facepalming with dark skin tone

		// Heart and kiss variations
		{"❤", "kiss"},  // U+2764
		{"❤️", "kiss"}, // U+2764 with VS16
		{"😘", "kiss"},
		{"🥰", "kiss"},

		// Fallback to "like"
		{"🎉", "like"},
		{"🔥", "like"},
		{"👏", "like"},
		{"custom_emoji", "like"},
		{"", "like"},
	} {
		if got := ReactionOf(c.in); got != c.want {
			t.Errorf("ReactionOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidReaction(t *testing.T) {
	for _, r := range Reactions {
		if !validReaction(r) {
			t.Errorf("validReaction(%q) = false, want true", r)
		}
	}
	for _, invalid := range []string{"love", "heart", "dislike", "thumbsup", "👍", "😍", "", "random"} {
		if validReaction(invalid) {
			t.Errorf("validReaction(%q) = true, want false", invalid)
		}
	}
}

func TestReactionEmojis(t *testing.T) {
	if got := (Count{Reaction: "laugh"}).Emoji(); got != "😂" {
		t.Errorf("Count.Emoji(laugh) = %q, want 😂", got)
	}
	if got := (Count{Reaction: "unknown"}).Emoji(); got != "" {
		t.Errorf("Count.Emoji(unknown) = %q, want empty", got)
	}

	if got := (Summary{Mine: "angry"}).MineEmoji(); got != "😡" {
		t.Errorf("Summary.MineEmoji(angry) = %q, want 😡", got)
	}
	if got := (Summary{Mine: ""}).MineEmoji(); got != "" {
		t.Errorf("Summary.MineEmoji(empty) = %q, want empty", got)
	}

	if got := (Reactor{Reaction: "wonder"}).Emoji(); got != "😮" {
		t.Errorf("Reactor.Emoji(wonder) = %q, want 😮", got)
	}
	if got := (Reactor{Reaction: ""}).Emoji(); got != "" {
		t.Errorf("Reactor.Emoji(empty) = %q, want empty", got)
	}
}
