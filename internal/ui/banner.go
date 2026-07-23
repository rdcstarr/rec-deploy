package ui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	colorful "github.com/lucasb-eyer/go-colorful"
	"golang.org/x/term"
)

// bannerArt is the "rec-deploy" wordmark (ANSI Shadow style). It is gradient-colored
// at render time, so it is stored as plain art here.
const bannerArt = `██████╗ ███████╗ ██████╗     ██████╗ ███████╗██████╗ ██╗      ██████╗ ██╗   ██╗
██╔══██╗██╔════╝██╔════╝     ██╔══██╗██╔════╝██╔══██╗██║     ██╔═══██╗╚██╗ ██╔╝
██████╔╝█████╗  ██║     ███╗ ██║  ██║█████╗  ██████╔╝██║     ██║   ██║ ╚████╔╝
██╔══██╗██╔══╝  ██║     ╚══╝ ██║  ██║██╔══╝  ██╔═══╝ ██║     ██║   ██║  ╚██╔╝
██║  ██║███████╗╚██████╗     ██████╔╝███████╗██║     ███████╗╚██████╔╝   ██║
╚═╝  ╚═╝╚══════╝ ╚═════╝     ╚═════╝ ╚══════╝╚═╝     ╚══════╝ ╚═════╝    ╚═╝   `

// bannerTagline is the one-line description shown under the wordmark.
const bannerTagline = "deploy GitHub repos in place"

// bannerStops are the gradient color stops blended left-to-right across the art.
var bannerStops = []colorful.Color{
	mustHex("#5B8CFF"), // blue
	mustHex("#9D7CFF"), // violet
	mustHex("#E26EE5"), // magenta
}

// Banner prints the rec-deploy welcome banner — a gradient wordmark with the tagline
// and version — shown before the interactive root menu.
func Banner(version string) {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil && width < 90 {
		Out(compactBannerString(version))
	} else {
		Out(bannerString(version))
	}
	Out("")
}

func compactBannerString(version string) string {
	tagline := bannerTagline
	if version != "" {
		tagline += "  ·  " + version
	}

	return "  " + render(StyleTitle, "rec-deploy") + "\n  " + render(StyleSubtle, tagline)
}

// bannerString renders the gradient wordmark (indented two spaces) above a
// subtle tagline + version line. Color is dropped for --no-color / NO_COLOR.
func bannerString(version string) string {
	lines := strings.Split(bannerArt, "\n")

	tagline := bannerTagline
	if version != "" {
		tagline += "  ·  " + version
	}

	width := 0
	for _, l := range lines {
		if w := len([]rune(l)); w > width {
			width = w
		}
	}

	var colHex []string
	if colorEnabled && width > 0 {
		colHex = make([]string, width)
		for x := range colHex {
			t := 0.0
			if width > 1 {
				t = float64(x) / float64(width-1)
			}
			colHex[x] = gradientAt(bannerStops, t).Hex()
		}
	}

	var b strings.Builder
	for _, line := range lines {
		b.WriteString("  ")
		for x, r := range []rune(line) {
			if r == ' ' || colHex == nil {
				b.WriteRune(r)
				continue
			}
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(colHex[x])).Render(string(r)))
		}
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString("  ")
	b.WriteString(render(StyleSubtle, tagline))

	return b.String()
}

// gradientAt returns the color at position t in [0,1] across the stops, blended
// in HCL space for a smooth, perceptually even fade.
func gradientAt(stops []colorful.Color, t float64) colorful.Color {
	if len(stops) == 1 {
		return stops[0]
	}

	seg := t * float64(len(stops)-1)
	i := int(seg)
	if i >= len(stops)-1 {
		return stops[len(stops)-1]
	}

	return stops[i].BlendHcl(stops[i+1], seg-float64(i)).Clamped()
}

// mustHex parses a hex color for a package-level stop; the inputs are constants.
func mustHex(s string) colorful.Color {
	c, _ := colorful.Hex(s)
	return c
}
