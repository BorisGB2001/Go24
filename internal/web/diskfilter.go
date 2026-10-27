package web

import (
	"net/url"
	"slices"
	"strings"
	"time"

	"go24/internal/drive"
)

// diskPeriods are the «Дата изменения» of the filter of a drive; range is from … to … (the days included).
var diskPeriods = []string{"today", "yesterday", "week", "month", "last_7", "last_30", "range"}

// diskFilter is the filter of a drive: the name or the text, the kind of the file, when it was last
// changed (a new version too), who uploaded it. Any of them set — the list is of the whole drive.
type diskFilter struct {
	Q, Type, Period, From, To, Author string
}

func diskFilterOf(qs url.Values) diskFilter {
	f := diskFilter{Q: strings.TrimSpace(qs.Get("q")), From: qs.Get("from"), To: qs.Get("to"), Author: qs.Get("author")}
	if t := qs.Get("type"); slices.Contains(fileTypes, t) {
		f.Type = t
	}
	if p := qs.Get("period"); slices.Contains(diskPeriods, p) {
		f.Period = p
	}
	if f.Period == "range" && f.From == "" && f.To == "" {
		f.Period = ""
	}
	return f
}

// On: something is set.
func (f diskFilter) On() bool { return f.Q != "" || f.Type != "" || f.Period != "" || f.Author != "" }

// Files: something but the name is set — the folders found by their name are not shown.
func (f diskFilter) Files() bool { return f.Type != "" || f.Period != "" || f.Author != "" }

// span is when the files kept were changed: [from, to), a zero time — no bound.
func (f diskFilter) span(now time.Time) (from, to time.Time) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch f.Period {
	case "today":
		return day, day.AddDate(0, 0, 1)
	case "yesterday":
		return day.AddDate(0, 0, -1), day
	case "week": // from Monday
		monday := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
		return monday, monday.AddDate(0, 0, 7)
	case "month":
		first := day.AddDate(0, 0, 1-day.Day())
		return first, first.AddDate(0, 1, 0)
	case "last_7":
		return day.AddDate(0, 0, -6), day.AddDate(0, 0, 1)
	case "last_30":
		return day.AddDate(0, 0, -29), day.AddDate(0, 0, 1)
	case "range":
		if d, err := time.ParseInLocation(time.DateOnly, f.From, now.Location()); err == nil {
			from = d
		}
		if d, err := time.ParseInLocation(time.DateOnly, f.To, now.Location()); err == nil {
			to = d.AddDate(0, 0, 1)
		}
	}
	return from, to
}

// Apply keeps the files that pass the filter but its name, which the search has had (drive.Search).
func (f diskFilter) Apply(files []drive.File, now time.Time) []drive.File {
	from, to := f.span(now)
	return slices.DeleteFunc(files, func(x drive.File) bool {
		return f.Type != "" && fileType(x.Name, x.ContentType) != f.Type ||
			f.Author != "" && x.UploaderID != f.Author ||
			!from.IsZero() && x.CreatedAt.Before(from) || !to.IsZero() && !x.CreatedAt.Before(to)
	})
}

// diskChip is a condition of the filter shown by the search: the page without it.
type diskChip struct {
	Key, Value, URL string // Key: q | type | period | author; Value: the text, the kind, the period or the person
}

// Chips are the conditions set, each with the address of the page without it (the order of the list and the folder
// kept).
func (f diskFilter) Chips(qs url.Values) []diskChip {
	without := func(keys ...string) string {
		v := url.Values{}
		for _, k := range []string{"q", "type", "period", "from", "to", "author", "sort", "desc"} {
			if qs.Get(k) != "" && !slices.Contains(keys, k) {
				v.Set(k, qs.Get(k))
			}
		}
		return "?" + v.Encode()
	}
	var out []diskChip
	if f.Q != "" {
		out = append(out, diskChip{"q", f.Q, without("q")})
	}
	if f.Type != "" {
		out = append(out, diskChip{"type", f.Type, without("type")})
	}
	if f.Period != "" {
		out = append(out, diskChip{"period", f.Period, without("period", "from", "to")})
	}
	if f.Author != "" {
		out = append(out, diskChip{"author", f.Author, without("author")})
	}
	return out
}
