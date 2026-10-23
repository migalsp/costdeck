package estimate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Marker starts every comment the estimate writes, so a CI job can find and update its
// own comment instead of adding one per push.
const Marker = "<!-- costdeck-estimate -->"

var symbols = map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥"}

// money writes an amount with thousands separators, in the currency's symbol when it has
// one.
func money(v float64, currency string) string {
	neg := v < 0
	cents := int64(math.Round(math.Abs(v) * 100))
	whole := strconv.FormatInt(cents/100, 10)
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	s := fmt.Sprintf("%s.%02d", whole, cents%100)
	if sym, ok := symbols[currency]; ok {
		s = sym + s
	} else {
		s += " " + currency
	}
	if neg {
		return "−" + s
	}
	return s
}

func signed(v float64, currency string) string {
	if v > 0 {
		return "+" + money(v, currency)
	}
	return money(v, currency)
}

// describe says what an item is and how it changes.
func (it Item) describe() string {
	what := fmt.Sprintf("%s `%s/%s`", it.Kind, it.Namespace, it.Name)
	switch {
	case it.Before == nil:
		return what + " (new)"
	case it.After == nil:
		return what + " (removed)"
	}
	var parts []string
	b, a := it.Before, it.After
	if b.Replicas != a.Replicas {
		parts = append(parts, fmt.Sprintf("%d → %d replicas", b.Replicas, a.Replicas))
	}
	if b.CPU != a.CPU {
		parts = append(parts, fmt.Sprintf("CPU %s → %s", b.CPU, a.CPU))
	}
	if b.Memory != a.Memory {
		parts = append(parts, fmt.Sprintf("memory %s → %s", b.Memory, a.Memory))
	}
	if b.GPU != a.GPU {
		parts = append(parts, fmt.Sprintf("GPUs %g → %g", b.GPU, a.GPU))
	}
	if b.StorageGiB != a.StorageGiB {
		parts = append(parts, fmt.Sprintf("storage %gGi → %gGi", b.StorageGiB, a.StorageGiB))
	}
	if len(parts) == 0 {
		return what
	}
	return what + ": " + strings.Join(parts, ", ")
}

// Markdown renders the estimate as a pull request comment.
func (r Result) Markdown() string {
	var b strings.Builder
	b.WriteString(Marker + "\n")
	cur := r.Currency
	switch {
	case len(r.Items) == 0:
		fmt.Fprintf(&b, "### Cost estimate: no change\n\nNothing this pull request changes costs anything more or less (%s a month).\n", money(r.After, cur))
		return b.String()
	case r.Before > 0:
		fmt.Fprintf(&b, "### Cost estimate: %s a month (%+.0f%%)\n\n", signed(r.Delta, cur), r.Delta/r.Before*100)
	default:
		fmt.Fprintf(&b, "### Cost estimate: %s a month\n\n", signed(r.Delta, cur))
	}
	b.WriteString("| Change | Before | After | Per month |\n|---|--:|--:|--:|\n")
	for _, it := range r.Items {
		before, after := "—", "—"
		if it.Before != nil {
			before = money(it.Before.Monthly, cur)
		}
		if it.After != nil {
			after = money(it.After.Monthly, cur)
		}
		desc := it.describe()
		if it.Note != "" {
			desc += " <sub>" + it.Note + "</sub>"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | **%s** |\n", desc, before, after, signed(it.Delta, cur))
	}
	fmt.Fprintf(&b, "\n%s a month before, %s after", money(r.Before, cur), money(r.After, cur))
	if r.Unchanged > 0 {
		fmt.Fprintf(&b, "; %d more object(s) unchanged", r.Unchanged)
	}
	b.WriteString(".\n")
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\n> %s", w)
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n<sub>Cost Deck estimate from requests at %s. Namespaces on a schedule count only the hours they run.</sub>\n", strings.ToLower(r.Basis))
	return b.String()
}
