package api

import (
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSleepPage(t *testing.T) {
	next := time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC)
	req := httptest.NewRequest(http.MethodGet, "http://shop-dev.example.com/cart?id=7", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("X-Forwarded-Proto", "https")
	d := sleepPageData{Namespace: "shop-dev", Schedule: "shop", NextStart: &next, WakeURL: wakeURL("https://costdeck.example.com/", "shop-dev", req)}
	if d.WakeURL != "https://costdeck.example.com/wake/shop-dev?return=https%3A%2F%2Fshop-dev.example.com%2Fcart%3Fid%3D7" {
		t.Errorf("wake URL = %s", d.WakeURL)
	}
	rr := httptest.NewRecorder()
	writeSleepPage(rr, req, d)
	body := rr.Body.String()
	if rr.Code != http.StatusServiceUnavailable || rr.Header().Get("Retry-After") != "60" || !strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
		t.Errorf("status %d, headers %v", rr.Code, rr.Header())
	}
	for _, want := range []string{"<title>shop-dev is asleep</title>", "<b>shop</b>", "until Mon 26 Oct, 07:00 UTC", `href="https://costdeck.example.com/wake/shop-dev?return=https%3A%2F%2Fshop-dev.example.com%2Fcart%3Fid%3D7"`, `content="20"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(strings.ToLower(rr.Header().Get("Set-Cookie")), "costdeck") {
		t.Error("the sleep page set a cookie")
	}

	// Without a dashboard URL there is no link; API clients get JSON.
	api := httptest.NewRequest(http.MethodGet, "http://shop-dev.example.com/api/items", nil)
	rr = httptest.NewRecorder()
	writeSleepPage(rr, api, sleepPageData{Namespace: "shop-dev", WakeURL: wakeURL("", "shop-dev", api)})
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"shop-dev is asleep`) || strings.Contains(rr.Body.String(), "<html") {
		t.Errorf("API client got %d %s", rr.Code, rr.Body.String())
	}
	// Names are escaped.
	rr = httptest.NewRecorder()
	writeSleepPage(rr, req, sleepPageData{Namespace: "<script>x</script>"})
	if strings.Contains(rr.Body.String(), "<script>x") {
		t.Error("the namespace was not escaped")
	}
}

// Every colour pair of the sleep page that carries text stays readable in both schemes:
// WCAG AA asks for a contrast of 4.5:1.
func TestSleepPageContrast(t *testing.T) {
	light, dark, ok := strings.Cut(sleepPageCSS, "@media (prefers-color-scheme:dark)")
	if !ok {
		t.Fatal("no dark scheme in the sleep page's CSS")
	}
	palette := func(css string) map[string]string {
		out := map[string]string{}
		for _, m := range regexp.MustCompile(`--([a-z-]+):(#[0-9a-f]{3,6})`).FindAllStringSubmatch(css, -1) {
			out[m[1]] = m[2]
		}
		return out
	}
	luminance := func(hex string) float64 {
		hex = strings.TrimPrefix(hex, "#")
		if len(hex) == 3 {
			hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
		}
		var l float64
		for i, w := range []float64{0.2126, 0.7152, 0.0722} {
			v, _ := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
			c := float64(v) / 255
			if c <= 0.03928 {
				c /= 12.92
			} else {
				c = math.Pow((c+0.055)/1.055, 2.4)
			}
			l += w * c
		}
		return l
	}
	contrast := func(a, b string) float64 {
		la, lb := luminance(a), luminance(b)
		return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
	}
	base := palette(light)
	for scheme, colours := range map[string]map[string]string{"light": base, "dark": palette(dark)} {
		for _, pair := range [][2]string{{"on-brand", "brand"}, {"text", "card"}, {"muted", "card"}, {"muted", "bg"}} {
			fg, bg := colours[pair[0]], colours[pair[1]]
			if fg == "" {
				fg = base[pair[0]]
			}
			if fg == "" || bg == "" {
				t.Errorf("%s: --%s or --%s is not set", scheme, pair[0], pair[1])
				continue
			}
			if c := contrast(fg, bg); c < 4.5 {
				t.Errorf("%s: --%s %s on --%s %s has a contrast of %.2f, below 4.5", scheme, pair[0], fg, pair[1], bg, c)
			}
		}
	}
}
