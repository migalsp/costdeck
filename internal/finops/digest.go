package finops

import (
	"fmt"
	"sort"
	"strings"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// The digest is the report every FinOps practice sends on a rhythm: what the last week or
// month cost against the one before, what the schedules saved, where the money went and
// what to do next. It is computed from the ledger, never written by a model, so engineers
// and finance read the same numbers.

// Digest periods.
const (
	PeriodWeek  = "week"
	PeriodMonth = "month"
)

const (
	digestNamespaces    = 8
	digestOpportunities = 5
	defaultDigestTime   = "09:00"
)

// Digest summarises one period against the one before.
type Digest struct {
	Period      string    `json:"period"`
	Title       string    `json:"title"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	PrevFrom    string    `json:"prevFrom"`
	PrevTo      string    `json:"prevTo"`
	GeneratedAt time.Time `json:"generatedAt"`
	Currency    string    `json:"currency"`
	// Hours and PrevHours are how much of each period the ledger observed.
	Hours     float64 `json:"hours"`
	PrevHours float64 `json:"prevHours"`
	// Complete is true when the ledger covers both periods.
	Complete bool `json:"complete"`
	// Cost is the whole bill: nodes, volumes and load balancers.
	Cost      float64 `json:"cost"`
	PrevCost  float64 `json:"prevCost"`
	Requested float64 `json:"requested"`
	Used      float64 `json:"used"`
	Saved     float64 `json:"saved"`
	PrevSaved float64 `json:"prevSaved"`
	// RunRate is the monthly cost at today's rates.
	RunRate       float64       `json:"runRate"`
	MonthToDate   MonthToDate   `json:"monthToDate"`
	Namespaces    []DigestRow   `json:"namespaces"`
	Teams         []DigestRow   `json:"teams"`
	Opportunities []Opportunity `json:"opportunities"`
}

// DigestRow is one namespace or team in a digest.
type DigestRow struct {
	Name string  `json:"name"`
	Cost float64 `json:"cost"`
	Prev float64 `json:"prev"`
}

// Change is the relative change against the previous period, or nil without one.
func (r DigestRow) Change() *float64 {
	if r.Prev <= 0 {
		return nil
	}
	c := r.Cost/r.Prev - 1
	return &c
}

// periodRange returns the inclusive date ranges of the last complete period and the one
// before it, as of now (UTC).
func periodRange(period string, now time.Time) (from, to, prevFrom, prevTo time.Time) {
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if period == PeriodMonth {
		thisMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		from = thisMonth.AddDate(0, -1, 0)
		to = thisMonth.AddDate(0, 0, -1)
		prevFrom = from.AddDate(0, -1, 0)
		prevTo = from.AddDate(0, 0, -1)
		return from, to, prevFrom, prevTo
	}
	to = today.AddDate(0, 0, -1)
	from = to.AddDate(0, 0, -6)
	prevTo = from.AddDate(0, 0, -1)
	prevFrom = prevTo.AddDate(0, 0, -6)
	return from, to, prevFrom, prevTo
}

// NewDigest builds the digest of the last complete week or month. The overview supplies
// today's run rate, the namespaces' teams and the opportunities.
func NewDigest(o Overview, l *Ledger, period string, now time.Time) Digest {
	if period != PeriodMonth {
		period = PeriodWeek
	}
	from, to, prevFrom, prevTo := periodRange(period, now)
	d := Digest{
		Period: period, GeneratedAt: now, Currency: o.Currency, RunRate: o.Monthly.Provisioned, MonthToDate: o.MonthToDate,
		From: DateOf(from), To: DateOf(to), PrevFrom: DateOf(prevFrom), PrevTo: DateOf(prevTo),
		Namespaces: []DigestRow{}, Teams: []DigestRow{}, Opportunities: []Opportunity{},
	}
	if period == PeriodMonth {
		d.Title = "Cost digest · " + from.Format("January 2006")
	} else {
		d.Title = fmt.Sprintf("Cost digest · %s – %s", from.Format("Jan 2"), to.Format("Jan 2"))
	}

	team := map[string]string{}
	for _, ns := range o.Namespaces {
		team[ns.Name] = ns.Team
	}
	cur, prev := map[string]float64{}, map[string]float64{}
	if l != nil {
		for _, day := range l.Days {
			switch {
			case day.Date >= d.From && day.Date <= d.To:
				d.Hours += day.Hours
				d.Cost += day.Bill()
				d.Requested += day.Requested
				d.Used += day.Used
				d.Saved += day.Saved
				for ns, v := range day.Namespaces {
					cur[ns] += v.Total()
				}
			case day.Date >= d.PrevFrom && day.Date <= d.PrevTo:
				d.PrevHours += day.Hours
				d.PrevCost += day.Bill()
				d.PrevSaved += day.Saved
				for ns, v := range day.Namespaces {
					prev[ns] += v.Total()
				}
			}
		}
	}
	days := func(a, b time.Time) float64 { return b.Sub(a).Hours() + 24 }
	d.Complete = d.Hours >= days(from, to)-1 && d.PrevHours >= days(prevFrom, prevTo)-1

	d.Namespaces = rows(cur, prev, digestNamespaces)
	teamCur, teamPrev := map[string]float64{}, map[string]float64{}
	for ns, v := range cur {
		teamCur[teamName(team[ns])] += v
	}
	for ns, v := range prev {
		teamPrev[teamName(team[ns])] += v
	}
	if len(teamCur) > 1 || (len(teamCur) == 1 && teamCur[teamName("")] == 0) {
		d.Teams = rows(teamCur, teamPrev, digestNamespaces)
	}
	for _, op := range o.Opportunities {
		if op.MonthlySavings > 0 && len(d.Opportunities) < digestOpportunities {
			d.Opportunities = append(d.Opportunities, op)
		}
	}
	return d
}

func teamName(t string) string {
	if t == "" {
		return "No team label"
	}
	return t
}

func rows(cur, prev map[string]float64, limit int) []DigestRow {
	out := make([]DigestRow, 0, len(cur))
	for name, v := range cur {
		if v > 0 {
			out = append(out, DigestRow{Name: name, Cost: v, Prev: prev[name]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Markdown renders the digest for a chat message or a file. Webex shows bold, italics and
// lists, so the digest uses no tables.
func (d Digest) Markdown() string {
	var b strings.Builder
	m := func(v float64) string { return money(v, d.Currency) }
	fmt.Fprintf(&b, "**%s**\n\n", d.Title)
	if d.Hours == 0 {
		b.WriteString("The cost history does not cover this period yet; Cost Deck started recording after it ended.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "The cluster cost **%s**%s. Pods requested %s of compute and used %s.\n",
		m(d.Cost), changeText(d.Cost, d.PrevCost, "the "+d.Period+" before"), m(d.Requested), m(d.Used))
	if d.Saved > 0 {
		fmt.Fprintf(&b, "Schedules saved **%s**%s.\n", m(d.Saved), changeText(d.Saved, d.PrevSaved, "the "+d.Period+" before"))
	}
	if d.MonthToDate.Hours > 0 {
		fmt.Fprintf(&b, "This month so far: %s, heading for %s at today's rates.\n", m(d.MonthToDate.Cost), m(d.MonthToDate.Forecast))
	}
	if !d.Complete {
		b.WriteString("_The cost history does not cover both periods completely, so the comparison is partial._\n")
	}
	if len(d.Namespaces) > 0 {
		b.WriteString("\n**Most expensive namespaces**\n")
		for _, r := range d.Namespaces {
			fmt.Fprintf(&b, "- %s: %s%s\n", r.Name, m(r.Cost), shortChange(r.Change()))
		}
	}
	if len(d.Teams) > 0 {
		b.WriteString("\n**By team**\n")
		for _, r := range d.Teams {
			fmt.Fprintf(&b, "- %s: %s%s\n", r.Name, m(r.Cost), shortChange(r.Change()))
		}
	}
	if len(d.Opportunities) > 0 {
		b.WriteString("\n**Worth doing**\n")
		for _, op := range d.Opportunities {
			fmt.Fprintf(&b, "- %s: about %s a month\n", op.Title, m(op.MonthlySavings))
		}
	}
	return b.String()
}

func changeText(cur, prev float64, against string) string {
	if prev <= 0 {
		return ""
	}
	c := cur/prev - 1
	switch {
	case c > 0.005:
		return fmt.Sprintf(" (%.0f%% more than %s)", c*100, against)
	case c < -0.005:
		return fmt.Sprintf(" (%.0f%% less than %s)", -c*100, against)
	}
	return fmt.Sprintf(" (about the same as %s)", against)
}

func shortChange(c *float64) string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf(" (%+.0f%%)", *c*100)
}

// NextDigest returns when a digest is due next after the given moment: Mondays for weekly,
// the 1st for monthly, at the schedule's time in its time zone.
func NextDigest(s finopsv1.DigestSchedule, after time.Time) (time.Time, error) {
	loc := time.UTC
	if s.Timezone != "" {
		l, err := time.LoadLocation(s.Timezone)
		if err != nil {
			return time.Time{}, fmt.Errorf("unknown time zone %q", s.Timezone)
		}
		loc = l
	}
	at := s.Time
	if at == "" {
		at = defaultDigestTime
	}
	hm, err := time.Parse("15:04", at)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q is not HH:MM", at)
	}
	local := after.In(loc)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
	if s.Frequency == "monthly" {
		candidate = time.Date(local.Year(), local.Month(), 1, hm.Hour(), hm.Minute(), 0, 0, loc)
		if !candidate.After(after) {
			candidate = time.Date(local.Year(), local.Month()+1, 1, hm.Hour(), hm.Minute(), 0, 0, loc)
		}
		return candidate, nil
	}
	for candidate.Weekday() != time.Monday || !candidate.After(after) {
		candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day()+1, hm.Hour(), hm.Minute(), 0, 0, loc)
	}
	return candidate, nil
}

// DigestPeriod is the period a schedule's digest covers.
func DigestPeriod(s finopsv1.DigestSchedule) string {
	if s.Frequency == "monthly" {
		return PeriodMonth
	}
	return PeriodWeek
}
