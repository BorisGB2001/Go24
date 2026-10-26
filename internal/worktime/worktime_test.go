package worktime

import (
	"testing"
	"time"
)

func TestDeadline(t *testing.T) {
	s := Default()
	at := func(v string) time.Time {
		x, err := time.ParseInLocation("2006-01-02 15:04", v, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	cases := []struct{ in, want string }{
		{"2026-10-14 15:00", "2026-10-14 15:00"}, // Wednesday, working hours: unchanged
		{"2026-10-17 12:00", "2026-10-19 18:00"}, // Saturday → Monday, end of day
		{"2026-10-14 20:00", "2026-10-14 18:00"}, // after hours → end of the day
		{"2026-10-14 07:00", "2026-10-14 18:00"}, // before hours → end of the day
		{"2026-11-04 12:00", "2026-11-05 18:00"}, // holiday (yearly 11-04) → next day
		{"2027-01-01 12:00", "2027-01-11 18:00"}, // New Year holidays and a weekend
	}
	for _, c := range cases {
		if got := s.Deadline(at(c.in)).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("Deadline(%s) = %s, want %s", c.in, got, c.want)
		}
	}
	if got := s.Day(at("2026-10-18 00:00")).Format("2006-01-02"); got != "2026-10-19" {
		t.Errorf("Day(Sunday) = %s", got)
	}
	if got := s.DefaultDeadline(at("2026-10-12 10:00")).Format("2006-01-02 15:04"); got != "2026-10-19 18:00" {
		t.Errorf("DefaultDeadline = %s", got)
	}
}

func TestValidate(t *testing.T) {
	bad := []Schedule{
		{Start: "9", End: "18:00"},
		{Start: "18:00", End: "09:00"},
		{Start: "09:00", End: "18:00", Weekend: []int{7}},
		{Start: "09:00", End: "18:00", Weekend: []int{0, 1, 2, 3, 4, 5, 6}},
		{Start: "09:00", End: "18:00", Holidays: []string{"31.12"}},
	}
	for _, s := range bad {
		if s.Validate() == nil {
			t.Errorf("%+v accepted", s)
		}
	}
	s := Schedule{Start: "09:00", End: "18:00", Holidays: []string{" 03-08", "", "01-01", "03-08"}}
	if err := s.Validate(); err != nil || len(s.Holidays) != 2 || s.Holidays[0] != "01-01" {
		t.Fatalf("%v %v", err, s.Holidays)
	}
}
