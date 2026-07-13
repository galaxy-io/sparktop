package metrics

import (
	"bufio"
	"io"
	"math"
	"strconv"
	"strings"
)

// sample is one line of the Prometheus text exposition format.
type sample struct {
	Labels map[string]string
	Value  float64
}

// family maps a metric name to all of its samples in a single scrape.
type family map[string][]sample

// first returns the value of the first sample of name, if any.
func (f family) first(name string) (float64, bool) {
	ss := f[name]
	if len(ss) == 0 {
		return 0, false
	}
	return ss[0].Value, true
}

// sum adds up every finite sample of name.
func (f family) sum(name string) float64 {
	var t float64
	for _, s := range f[name] {
		if isFinite(s.Value) {
			t += s.Value
		}
	}
	return t
}

// sumWhere adds up every finite sample of name whose labels satisfy pred.
func (f family) sumWhere(name string, pred func(map[string]string) bool) float64 {
	var t float64
	for _, s := range f[name] {
		if isFinite(s.Value) && pred(s.Labels) {
			t += s.Value
		}
	}
	return t
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// parseText reads a Prometheus text-format /metrics body. It is intentionally
// small: it handles what node_exporter and dcgm-exporter actually emit (no
// histograms/summaries needed, quoted label values with \\ \" \n escapes).
func parseText(r io.Reader) (family, error) {
	fam := family{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		name, s, ok := parseLine(line)
		if !ok {
			continue
		}
		fam[name] = append(fam[name], s)
	}
	return fam, sc.Err()
}

func parseLine(line string) (string, sample, bool) {
	s := sample{Labels: map[string]string{}}
	i := 0
	for i < len(line) && isNameByte(line[i]) {
		i++
	}
	if i == 0 {
		return "", s, false
	}
	name := line[:i]
	rest := line[i:]
	if len(rest) > 0 && rest[0] == '{' {
		rest = parseLabels(rest, s.Labels)
	}
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return "", s, false
	}
	valStr := rest
	if j := strings.IndexAny(rest, " \t"); j >= 0 {
		valStr = rest[:j]
	}
	v, ok := parseFloat(valStr)
	if !ok {
		return "", s, false
	}
	s.Value = v
	return name, s, true
}

// parseLabels consumes a "{...}" section starting at s[0]=='{' and returns the
// remainder of the line after the closing '}'.
func parseLabels(s string, out map[string]string) string {
	i := 1 // skip '{'
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == ',' || s[i] == '\t') {
			i++
		}
		if i < len(s) && s[i] == '}' {
			return s[i+1:]
		}
		start := i
		for i < len(s) && isNameByte(s[i]) {
			i++
		}
		key := s[start:i]
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			return s[i:]
		}
		i++
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) || s[i] != '"' {
			return s[i:]
		}
		i++
		var b strings.Builder
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case '"', '\\':
					b.WriteByte(s[i])
				default:
					b.WriteByte('\\')
					b.WriteByte(s[i])
				}
				i++
				continue
			}
			if c == '"' {
				i++
				break
			}
			b.WriteByte(c)
			i++
		}
		if key != "" {
			out[key] = b.String()
		}
	}
	return ""
}

func isNameByte(b byte) bool {
	return b == '_' || b == ':' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func parseFloat(s string) (float64, bool) {
	switch s {
	case "+Inf", "Inf":
		return math.Inf(1), true
	case "-Inf":
		return math.Inf(-1), true
	case "NaN":
		return math.NaN(), true
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}
