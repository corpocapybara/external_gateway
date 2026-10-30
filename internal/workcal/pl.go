// Package workcal is the Polish working-day calendar behind the worklog norm:
// Monday to Friday, minus statutory public holidays (ustawa o dniach wolnych od
// pracy). A holiday on a Saturday or Sunday changes nothing.
package workcal

import "time"

// HoursPerDay is the daily norm a working day contributes.
const HoursPerDay = 8

// Holiday is one Polish public holiday.
type Holiday struct {
	Date time.Time // midnight UTC of the calendar date
	Name string
}

// Date returns midnight UTC of a calendar date, the form every function here takes.
func Date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// Civil returns the calendar date of t in t's own location, as midnight UTC.
func Civil(t time.Time) time.Time {
	return Date(t.Year(), t.Month(), t.Day())
}

// easter returns Easter Sunday of a Gregorian year (anonymous Gregorian algorithm).
func easter(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return Date(year, time.Month(month), day)
}

// PolishHolidays lists the public holidays of a year in date order.
func PolishHolidays(year int) []Holiday {
	e := easter(year)
	hs := []Holiday{
		{Date(year, time.January, 1), "Nowy Rok"},
		{Date(year, time.January, 6), "Trzech Króli"},
		{e, "Wielkanoc"},
		{e.AddDate(0, 0, 1), "Poniedziałek Wielkanocny"},
		{Date(year, time.May, 1), "Święto Pracy"},
		{Date(year, time.May, 3), "Święto Konstytucji 3 Maja"},
		{e.AddDate(0, 0, 49), "Zielone Świątki"},
		{e.AddDate(0, 0, 60), "Boże Ciało"},
		{Date(year, time.August, 15), "Wniebowzięcie NMP"},
		{Date(year, time.November, 1), "Wszystkich Świętych"},
		{Date(year, time.November, 11), "Narodowe Święto Niepodległości"},
	}
	// Wigilia is a public holiday from 2025 (Dz.U. 2024 poz. 1965).
	if year >= 2025 {
		hs = append(hs, Holiday{Date(year, time.December, 24), "Wigilia"})
	}
	hs = append(hs,
		Holiday{Date(year, time.December, 25), "Boże Narodzenie"},
		Holiday{Date(year, time.December, 26), "Drugi dzień Bożego Narodzenia"},
	)
	return hs
}

// HolidayName reports whether a calendar date is a public holiday, and its name.
func HolidayName(d time.Time) (string, bool) {
	d = Civil(d)
	for _, h := range PolishHolidays(d.Year()) {
		if h.Date.Equal(d) {
			return h.Name, true
		}
	}
	return "", false
}

// IsWorkingDay reports whether a calendar date is Monday to Friday and not a holiday.
func IsWorkingDay(d time.Time) bool {
	if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	_, holiday := HolidayName(d)
	return !holiday
}

// WorkingDays counts the working days from one calendar date to another, both
// included. It is 0 when to is before from.
func WorkingDays(from, to time.Time) int {
	n := 0
	for d := Civil(from); !d.After(Civil(to)); d = d.AddDate(0, 0, 1) {
		if IsWorkingDay(d) {
			n++
		}
	}
	return n
}

// HolidaysBetween lists the public holidays from one calendar date to another,
// both included, weekend ones too.
func HolidaysBetween(from, to time.Time) []Holiday {
	from, to = Civil(from), Civil(to)
	var out []Holiday
	for y := from.Year(); y <= to.Year(); y++ {
		for _, h := range PolishHolidays(y) {
			if !h.Date.Before(from) && !h.Date.After(to) {
				out = append(out, h)
			}
		}
	}
	return out
}
