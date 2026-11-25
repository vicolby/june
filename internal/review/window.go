package review

import (
	"sort"
	"strconv"
	"strings"
)

// contextLines is how many lines around a touched span the model sees.
const contextLines = 25

// window is the text of one file the model reviews: its touched lines marked, with context around them.
type window struct {
	path    string
	text    string
	touched []Span // clamped to the file and sorted, merged
}

// lines splits source into lines the way a reader counts them: a final line break ends a line, it does not start another.
func lines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text == "" {
		return nil
	}
	text = strings.TrimSuffix(text, "\n")
	return strings.Split(text, "\n")
}

// mergeSpans clamps spans to 1..n, sorts them and joins those that overlap or touch.
func mergeSpans(spans []Span, n int) []Span {
	var in []Span
	for _, s := range spans {
		from, to := max(s.From, 1), min(s.To, n)
		if from <= to {
			in = append(in, Span{from, to})
		}
	}
	sort.Slice(in, func(i, j int) bool { return in[i].From < in[j].From })
	var out []Span
	for _, s := range in {
		if k := len(out) - 1; k >= 0 && s.From <= out[k].To+1 {
			out[k].To = max(out[k].To, s.To)
			continue
		}
		out = append(out, s)
	}
	return out
}

// newWindow renders src, or reports false when none of its touched lines is inside the file.
// Every code line carries its number, so source text cannot forge the structure around it.
func newWindow(src Source) (window, bool) {
	ls := lines(src.Text)
	touched := mergeSpans(src.Touched, len(ls))
	if len(touched) == 0 {
		return window{}, false
	}
	var shown []Span
	for _, s := range touched {
		from, to := max(s.From-contextLines, 1), min(s.To+contextLines, len(ls))
		if k := len(shown) - 1; k >= 0 && from <= shown[k].To+1 {
			shown[k].To = max(shown[k].To, to)
			continue
		}
		shown = append(shown, Span{from, to})
	}
	width := len(strconv.Itoa(len(ls)))
	var b strings.Builder
	b.WriteString("FILE " + src.Path + "\n")
	next := 0 // index into touched
	for i, s := range shown {
		if i > 0 {
			b.WriteString("...\n")
		}
		for n := s.From; n <= s.To; n++ {
			for next < len(touched) && touched[next].To < n {
				next++
			}
			mark := " "
			if next < len(touched) && touched[next].From <= n {
				mark = ">"
			}
			b.WriteString(mark + " " + strings.Repeat(" ", width-len(strconv.Itoa(n))) + strconv.Itoa(n) + "| " + ls[n-1] + "\n")
		}
	}
	return window{path: src.Path, text: b.String(), touched: touched}, true
}

func (w window) has(line int) bool {
	for _, s := range w.touched {
		if s.From <= line && line <= s.To {
			return true
		}
	}
	return false
}
