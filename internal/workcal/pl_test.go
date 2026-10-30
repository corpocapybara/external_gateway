package workcal

import (
	"testing"
	"time"
)

func TestEaster(t *testing.T) {
	for year, want := range map[int]time.Time{
		2024: Date(2024, time.March, 31),
		2025: Date(2025, time.April, 20),
		2026: Date(2026, time.April, 5),
		2027: Date(2027, time.March, 28),
	} {
		if got := easter(year); !got.Equal(want) {
			t.Errorf("easter(%d) = %s, want %s", year, got.Format("2006-01-02"), want.Format("2006-01-02"))
		}
	}
}

func TestHolidayName(t *testing.T) {
	cases := []struct {
		date time.Time
		want string
	}{
		{Date(2026, time.June, 4), "Boże Ciało"},
		{Date(2026, time.April, 6), "Poniedziałek Wielkanocny"},
		{Date(2026, time.May, 24), "Zielone Świątki"},
		{Date(2025, time.December, 24), "Wigilia"},
		{Date(2026, time.November, 11), "Narodowe Święto Niepodległości"},
	}
	for _, c := range cases {
		if got, ok := HolidayName(c.date); !ok || got != c.want {
			t.Errorf("HolidayName(%s) = %q, %v; want %q", c.date.Format("2006-01-02"), got, ok, c.want)
		}
	}
	// Wigilia became a holiday in 2025; before that 24 December was a working day.
	if _, ok := HolidayName(Date(2024, time.December, 24)); ok {
		t.Error("24 December 2024 must not be a holiday")
	}
	if _, ok := HolidayName(Date(2026, time.October, 30)); ok {
		t.Error("30 October 2026 must not be a holiday")
	}
}

// Month norms: Monday-Friday minus holidays; a holiday on a weekend lowers nothing.
func TestWorkingDaysPerMonth(t *testing.T) {
	cases := []struct {
		year  int
		month time.Month
		want  int
	}{
		{2024, time.December, 20}, // 25, 26 on Wed/Thu; no Wigilia yet
		{2025, time.January, 21},  // 1 Jan Wed, 6 Jan Mon
		{2026, time.May, 20},      // 1 May Fri; 3 May and Zielone Świątki fall on Sunday
		{2026, time.June, 21},     // Boże Ciało Thu 4 Jun
		{2026, time.October, 22},  // no holidays
		{2026, time.November, 20}, // 1 Nov Sun, 11 Nov Wed
		{2026, time.December, 21}, // 24 Thu, 25 Fri; 26 Sat lowers nothing
	}
	for _, c := range cases {
		from := Date(c.year, c.month, 1)
		to := from.AddDate(0, 1, -1)
		if got := WorkingDays(from, to); got != c.want {
			t.Errorf("WorkingDays(%d-%02d) = %d, want %d", c.year, c.month, got, c.want)
		}
	}
}

func TestWorkingDaysRange(t *testing.T) {
	if got := WorkingDays(Date(2026, time.October, 26), Date(2026, time.November, 1)); got != 5 {
		t.Errorf("week of 26 Oct 2026 = %d, want 5", got)
	}
	if got := WorkingDays(Date(2026, time.November, 9), Date(2026, time.November, 15)); got != 4 {
		t.Errorf("week of 9 Nov 2026 = %d, want 4", got)
	}
	if got := WorkingDays(Date(2026, time.November, 2), Date(2026, time.November, 1)); got != 0 {
		t.Errorf("reversed range = %d, want 0", got)
	}
	// Civil keeps the local calendar date: 00:30 in Warsaw is still that day.
	waw, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	if got := Civil(time.Date(2026, time.November, 11, 0, 30, 0, 0, waw)); !got.Equal(Date(2026, time.November, 11)) {
		t.Errorf("Civil = %s", got)
	}
}

func TestHolidaysBetweenSpansYears(t *testing.T) {
	hs := HolidaysBetween(Date(2025, time.December, 29), Date(2026, time.January, 6))
	if len(hs) != 2 || hs[0].Name != "Nowy Rok" || hs[1].Name != "Trzech Króli" {
		t.Fatalf("HolidaysBetween = %v", hs)
	}
}
