package rules

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Python reads source in the encoding its first two lines declare (PEP 263), UTF-8 when none does.
// ast-grep only reads UTF-8, so a file is transcoded before it is staged. The supported single-byte
// encodings leave line-break bytes unchanged, and UTF-8 is kept as-is, preserving line numbers.
// Columns use ast-grep's positions in the UTF-8 text. A bare carriage return is a Python line ending
// the scanner does not share, so a file that contains one is skipped rather than shifted.

// codingDeclaration is the PEP 263 pattern, matched against one line.
var codingDeclaration = regexp.MustCompile(`^[ \t\f]*#.*?coding[:=][ \t]*([-_.a-zA-Z0-9]+)`)

// blankOrComment matches a first line that allows the declaration to sit on the second.
var blankOrComment = regexp.MustCompile(`^[ \t\f]*(?:[#\r\n]|$)`)

// declaredEncoding returns the coding name declared on the first or second line, "" when there is none.
func declaredEncoding(source []byte) string {
	first, rest := cutLine(source)
	if m := codingDeclaration.FindSubmatch(first); m != nil {
		return string(m[1])
	}
	if !blankOrComment.Match(first) {
		return ""
	}
	second, _ := cutLine(rest)
	if m := codingDeclaration.FindSubmatch(second); m != nil {
		return string(m[1])
	}
	return ""
}

// cutLine splits off the first line, ended by \n, \r\n or a lone \r as Python reads them.
func cutLine(b []byte) (line, rest []byte) {
	i := bytes.IndexAny(b, "\r\n")
	if i < 0 {
		return b, nil
	}
	n := i + 1
	if b[i] == '\r' && n < len(b) && b[n] == '\n' {
		n++
	}
	return b[:i], b[n:]
}

// bareCR reports a carriage return that does not begin a CRLF pair.
// Python ends a physical line at that byte; the scanner only ends a line at LF.
func bareCR(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if b[i] == '\r' && (i+1 == len(b) || b[i+1] != '\n') {
			return true
		}
	}
	return false
}

type codec int

const (
	codecUTF8 codec = iota
	codecLatin1
	codecASCII
	codecCP1252
	codecUnknown
)

// codecOf maps a declared name, as Python's codec registry spells it, to a supported encoding.
func codecOf(name string) codec {
	switch strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(name)) {
	case "utf8", "u8", "utf", "utf8sig":
		return codecUTF8
	case "latin1", "latin", "l1", "iso88591", "iso8859", "8859", "isolatin1", "cp819", "isoir100":
		return codecLatin1
	case "ascii", "usascii", "646":
		return codecASCII
	case "cp1252", "windows1252", "1252":
		return codecCP1252
	}
	return codecUnknown
}

// cp1252High is windows-1252 for bytes 0x80-0x9f; 0 marks a byte the encoding leaves undefined.
var cp1252High = [32]rune{
	0x20AC, 0, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017D, 0,
	0, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0, 0x017E, 0x0178,
}

// toUTF8 returns source as UTF-8 text, or the reason it cannot be read the way Python would read it.
func toUTF8(source []byte) ([]byte, string) {
	if bytes.IndexByte(source, 0) >= 0 {
		return nil, "it contains NUL bytes"
	}
	name := declaredEncoding(bytes.TrimPrefix(source, []byte("\xef\xbb\xbf")))
	c := codecUTF8
	if name != "" {
		c = codecOf(name)
	}
	if bytes.HasPrefix(source, []byte("\xef\xbb\xbf")) && c != codecUTF8 {
		return nil, fmt.Sprintf("it starts with a UTF-8 byte order mark but declares %q", name)
	}
	var text []byte
	switch c {
	case codecUTF8:
		if !utf8.Valid(source) {
			if name != "" {
				return nil, fmt.Sprintf("it declares %q but is not valid UTF-8", name)
			}
			return nil, "it is not valid UTF-8 and declares no other encoding"
		}
		text = source
	case codecLatin1, codecCP1252, codecASCII:
		out := make([]byte, 0, len(source)+len(source)/8)
		for _, b := range source {
			r := rune(b)
			switch {
			case b < 0x80:
			case c == codecASCII:
				return nil, fmt.Sprintf("it declares %q but holds bytes above 0x7f", name)
			case c == codecCP1252 && b < 0xa0:
				if r = cp1252High[b-0x80]; r == 0 {
					return nil, fmt.Sprintf("it declares %q but holds the undefined byte 0x%02x", name, b)
				}
			}
			out = utf8.AppendRune(out, r)
		}
		text = out
	default:
		return nil, fmt.Sprintf("it declares the encoding %q, which june cannot read (supported: utf-8, latin-1, ascii, cp1252)", name)
	}
	if bareCR(text) {
		return nil, "it uses bare carriage returns as line endings"
	}
	return text, ""
}
