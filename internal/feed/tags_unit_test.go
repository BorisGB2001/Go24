package feed

import (
	"slices"
	"strings"
	"testing"
)

func TestNormTag(t *testing.T) {
	for _, c := range []struct {
		in, want string
	}{
		{"tag", "tag"},
		{"#tag", "tag"},
		{"###tag", "tag"},
		{"  #TAG  ", "tag"},
		{"  tag   with    spaces  ", "tag with spaces"},
		{"#tag-with-hyphens-", "tag-with-hyphens"},
		{"#---", ""},
		{"", ""},
		{"   ", ""},
		{"###", ""},
		{"###Tag#Name", "tag#name"},
		{"#ПриветМир", "приветмир"},
		{"#Отчёт   по   проекту-", "отчёт по проекту"},
		{strings.Repeat("a", 60), strings.Repeat("a", 50)},
		{strings.Repeat("a", 49) + "-bbbb", strings.Repeat("a", 49)},
		{strings.Repeat("ж", 60), strings.Repeat("ж", 50)},
	} {
		if got := NormTag(c.in); got != c.want {
			t.Errorf("NormTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHashtags(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		{"hello #world", []string{"world"}},
		{"#first #second #first", []string{"first", "second"}},
		{"(#paren) [#bracket] «#quotes» \"#dquotes\"", []string{"paren", "bracket", "quotes", "dquotes"}},
		{"link /feed#notatag and html &#39;notatag", nil},
		{"pure numbers #123 #456", nil},
		{"alphanumeric #123a #task_1 #2026-goals-", []string{"123a", "task_1", "2026-goals"}},
		{"Unicode #отчёт #план_2026", []string{"отчёт", "план_2026"}},
		{"punctuation #tag, #tag. #tag! #tag? #tag: #tag;", []string{"tag"}},
		{"", nil},
	} {
		got := Hashtags(c.text)
		if !slices.Equal(got, c.want) {
			t.Errorf("Hashtags(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestParseTags(t *testing.T) {
	for _, c := range []struct {
		s    string
		want []string
	}{
		{"tag1, tag2, tag3", []string{"tag1", "tag2", "tag3"}},
		{"tag1; tag2; tag3", []string{"tag1", "tag2", "tag3"}},
		{"tag1\ntag2\ntag3", []string{"tag1", "tag2", "tag3"}},
		{"tag1, tag2; tag3\ntag4", []string{"tag1", "tag2", "tag3", "tag4"}},
		{"space tag, other space tag", []string{"space tag", "other space tag"}},
		{"tag1 tag2   tag3", []string{"tag1", "tag2", "tag3"}},
		{"tag1\ttag2", []string{"tag1", "tag2"}},
		{"#tag1, #tag2, tag1", []string{"tag1", "tag2"}},
		{"  , ; \n  ", nil},
		{"", nil},
	} {
		got := ParseTags(c.s)
		if !slices.Equal(got, c.want) {
			t.Errorf("ParseTags(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestAppendTag(t *testing.T) {
	if got := appendTag(nil, "a"); !slices.Equal(got, []string{"a"}) {
		t.Errorf("appendTag(nil, a) = %v", got)
	}
	if got := appendTag([]string{"a"}, "b"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("appendTag([a], b) = %v", got)
	}
	if got := appendTag([]string{"a", "b"}, "a"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("appendTag([a, b], a) = %v", got)
	}
}

func TestPostTypedTags(t *testing.T) {
	for _, c := range []struct {
		name string
		post Post
		want string
	}{
		{"empty", Post{}, ""},
		{"typed only", Post{Tags: []string{"dev", "go"}}, "dev, go"},
		{"hashtags in text", Post{
			Title: "Title #dev",
			Body:  "Body #news",
			Tags:  []string{"dev", "news", "urgent"},
		}, "urgent"},
		{"all in text", Post{
			Title: "Title #dev",
			Body:  "#news",
			Tags:  []string{"dev", "news"},
		}, ""},
		{"different tags", Post{
			Title: "Title #dev",
			Tags:  []string{"ops", "infra"},
		}, "ops, infra"},
	} {
		if got := c.post.TypedTags(); got != c.want {
			t.Errorf("%s: TypedTags() = %q, want %q", c.name, got, c.want)
		}
	}
}
