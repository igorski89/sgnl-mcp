package tools

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teambition/rrule-go"
)

// Repeating events. The mail server's CalendarEvent/query ignores
// expandRecurrences and finds a series only by its first occurrence
// (Stalwart 1.1, probed 2026-11-26), so the gateway expands series itself:
// it keeps each person's series (whole objects: a properties list drops
// the rules) per calendar state, and lists the occurrences in a window.

const localFmt = "2006-01-02T15:04:05"

// occurrence: one meeting of a series, as it really is (overrides applied).
type occurrence struct {
	RecurrenceID string // the occurrence's original local start (the override key)
	Start        time.Time
	Title        string
	Duration     string
	Status       string
	Locations    map[string]struct{ Name string }
	Moved        bool
}

var weekdays = map[string]rrule.Weekday{"mo": rrule.MO, "tu": rrule.TU, "we": rrule.WE, "th": rrule.TH, "fr": rrule.FR, "sa": rrule.SA, "su": rrule.SU}

// ruleOf: a JSCalendar RecurrenceRule as rrule options; an error for any
// part the gateway can't follow exactly (it then doesn't guess).
func ruleOf(r map[string]any, start time.Time) (*rrule.RRule, error) {
	o := rrule.ROption{Dtstart: start}
	ints := func(k string) ([]int, error) {
		var out []int
		vs, _ := r[k].([]any)
		for _, v := range vs {
			switch x := v.(type) {
			case float64:
				out = append(out, int(x))
			case string: // byMonth: "1".."12" ("3L" leap months: not followed)
				n, err := strconv.Atoi(x)
				if err != nil {
					return nil, fmt.Errorf("%s %q", k, x)
				}
				out = append(out, n)
			default:
				return nil, fmt.Errorf("%s", k)
			}
		}
		return out, nil
	}
	for k, v := range r {
		var err error
		switch k {
		case "@type":
		case "frequency":
			f, ok := map[string]rrule.Frequency{"yearly": rrule.YEARLY, "monthly": rrule.MONTHLY, "weekly": rrule.WEEKLY, "daily": rrule.DAILY,
				"hourly": rrule.HOURLY, "minutely": rrule.MINUTELY, "secondly": rrule.SECONDLY}[fmt.Sprint(v)]
			if !ok {
				return nil, fmt.Errorf("frequency %v", v)
			}
			o.Freq = f
		case "interval":
			n, _ := v.(float64)
			o.Interval = int(n)
		case "count":
			n, _ := v.(float64)
			o.Count = int(n)
		case "until":
			u, perr := time.ParseInLocation(localFmt, fmt.Sprint(v), start.Location())
			if perr != nil {
				return nil, fmt.Errorf("until %v", v)
			}
			o.Until = u
		case "firstDayOfWeek":
			w, ok := weekdays[fmt.Sprint(v)]
			if !ok {
				return nil, fmt.Errorf("firstDayOfWeek %v", v)
			}
			o.Wkst = w
		case "byDay":
			ds, _ := v.([]any)
			for _, d := range ds {
				m, _ := d.(map[string]any)
				w, ok := weekdays[fmt.Sprint(m["day"])]
				if !ok {
					return nil, fmt.Errorf("byDay %v", m["day"])
				}
				if n, ok := m["nthOfPeriod"].(float64); ok {
					w = w.Nth(int(n))
				}
				o.Byweekday = append(o.Byweekday, w)
			}
		case "byMonthDay":
			o.Bymonthday, err = ints(k)
		case "byMonth":
			o.Bymonth, err = ints(k)
		case "byYearDay":
			o.Byyearday, err = ints(k)
		case "byWeekNo":
			o.Byweekno, err = ints(k)
		case "byHour":
			o.Byhour, err = ints(k)
		case "byMinute":
			o.Byminute, err = ints(k)
		case "bySecond":
			o.Bysecond, err = ints(k)
		case "bySetPosition":
			o.Bysetpos, err = ints(k)
		case "rscale":
			if fmt.Sprint(v) != "gregorian" {
				return nil, fmt.Errorf("rscale %v", v)
			}
		case "skip":
			if fmt.Sprint(v) != "omit" {
				return nil, fmt.Errorf("skip %v", v)
			}
		default:
			return nil, fmt.Errorf("%s", k)
		}
		if err != nil {
			return nil, err
		}
	}
	if o.Interval == 0 {
		o.Interval = 1
	}
	return rrule.NewRRule(o)
}

// errUnclear: a series the gateway can't expand exactly.
type errUnclear struct{ why string }

func (e errUnclear) Error() string { return "repeat pattern not followed here: " + e.why }

// occurrences: the meetings of series e that overlap [from, to), oldest
// first, with the overrides applied (moved, changed, cancelled; excluded
// ones left out). At most 500.
func (t *Tools) occurrences(e calEvent, from, to time.Time) ([]occurrence, error) {
	start, end, err := t.window(e)
	if err != nil {
		return nil, err
	}
	length := end.Sub(start)
	set := &rrule.Set{}
	set.DTStart(start)
	for _, r := range e.RecurrenceRules {
		m, _ := r.(map[string]any)
		rr, err := ruleOf(m, start)
		if err != nil {
			return nil, errUnclear{err.Error()}
		}
		set.RRule(rr)
	}
	var ex []*rrule.RRule
	for _, r := range e.ExRules {
		m, _ := r.(map[string]any)
		rr, err := ruleOf(m, start)
		if err != nil {
			return nil, errUnclear{"excluded " + err.Error()}
		}
		ex = append(ex, rr)
	}
	if len(e.RecurrenceRules) == 0 {
		set.RDate(start) // a series of overrides only
	}
	// Generated starts, a day of slack before (long events, moved ones).
	gen := map[string]time.Time{}
	lo, hi := from.Add(-length).AddDate(0, 0, -1), to.AddDate(0, 0, 1)
	for _, s := range set.Between(lo, hi, true) {
		gen[s.Format(localFmt)] = s
	}
	for _, rr := range ex {
		for _, s := range rr.Between(lo, hi, true) {
			delete(gen, s.Format(localFmt))
		}
	}
	// Overrides: also those of occurrences generated outside the window
	// (moved into it) and added dates (keys the rule doesn't make).
	for k := range e.RecurrenceOver {
		if _, ok := gen[k]; !ok {
			if s, err := time.ParseInLocation(localFmt, k, start.Location()); err == nil {
				gen[k] = s
			}
		}
	}
	var out []occurrence
	for _, k := range slices.Sorted(maps.Keys(gen)) {
		o := occurrence{RecurrenceID: k, Start: gen[k], Title: e.Title, Duration: e.Duration, Status: e.Status, Locations: e.Locations}
		if p, ok := e.RecurrenceOver[k]; ok {
			if x, _ := p["excluded"].(bool); x {
				continue
			}
			if s, ok := p["start"].(string); ok {
				if st, err := time.ParseInLocation(localFmt, s, start.Location()); err == nil {
					o.Start, o.Moved = st, !st.Equal(gen[k])
				}
			}
			if v, ok := p["title"].(string); ok {
				o.Title = v
			}
			if v, ok := p["duration"].(string); ok {
				o.Duration = v
			}
			if v, ok := p["status"].(string); ok {
				o.Status = v
			}
			if v, ok := p["locations"].(map[string]any); ok {
				o.Locations = map[string]struct{ Name string }{}
				for id, l := range v {
					lm, _ := l.(map[string]any)
					o.Locations[id] = struct{ Name string }{fmt.Sprint(lm["name"])}
				}
			}
		}
		d := length
		if o.Duration != e.Duration {
			_, oe, _ := t.window(calEvent{Start: o.Start.Format(localFmt), TimeZone: e.TimeZone, Duration: o.Duration})
			d = oe.Sub(o.Start.In(oe.Location()))
		}
		if o.Start.Before(to) && (o.Start.Add(d).After(from) || (d == 0 && !o.Start.Before(from))) {
			out = append(out, o)
		}
		if len(out) >= 500 {
			break
		}
	}
	return out, nil
}

// seriesCache: each person's repeating events, per mail account, valid
// while the calendar's state is unchanged.
type seriesCache struct {
	mu sync.Mutex
	m  map[string]seriesEntry
}

type seriesEntry struct {
	state     string
	series    []calEvent
	truncated bool
}

func (c *seriesCache) get(acct, state string) (seriesEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[acct]
	return e, ok && state != "" && e.state == state
}

func (c *seriesCache) put(acct string, e seriesEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 500 {
		c.m = map[string]seriesEntry{}
	}
	c.m[acct] = e
}

func locNames(ls map[string]struct{ Name string }) string {
	for _, id := range slices.Sorted(maps.Keys(ls)) {
		if n := strings.TrimSpace(ls[id].Name); n != "" {
			return n
		}
	}
	return ""
}
