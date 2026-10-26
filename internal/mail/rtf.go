package mail

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message/charset"
)

// A letter written in Outlook often keeps its text only as compressed RTF (PR_RTF_COMPRESSED, [MS-OXRTFCP]), and a
// letter that came as HTML keeps that HTML inside the RTF ([MS-OXRTFEX], «\fromhtml1»): rtfHTML brings it back for
// the import of .msg and .pst files.

var errRTF = errors.New("not compressed RTF")

// rtfPrebuf is what the dictionary of the compressed RTF starts with.
const rtfPrebuf = "{\\rtf1\\ansi\\mac\\deff0\\deftab720{\\fonttbl;}{\\f0\\fnil \\froman \\fswiss \\fmodern \\fscript \\fdecor MS Sans SerifSymbolArialTimes New RomanCourier{\\colortbl\\red0\\green0\\blue0\r\n\\par \\pard\\plain\\f0\\fs20\\b\\i\\u\\tab\\tx"

// rtfDecompress unpacks PR_RTF_COMPRESSED: LZFu, or «MELA» (stored as it is).
func rtfDecompress(b []byte) ([]byte, error) {
	if len(b) < 16 {
		return nil, errRTF
	}
	size := int(binary.LittleEndian.Uint32(b[4:]))
	data := b[16:]
	switch string(b[8:12]) {
	case "MELA":
		if size > len(data) {
			size = len(data)
		}
		return data[:size], nil
	case "LZFu":
	default:
		return nil, errRTF
	}
	if size > 64<<20 {
		return nil, errRTF
	}
	var dict [4096]byte
	copy(dict[:], rtfPrebuf)
	w := len(rtfPrebuf)
	out := make([]byte, 0, size)
	for i := 0; i < len(data); {
		ctrl := data[i]
		i++
		for bit := 0; bit < 8 && i < len(data); bit++ {
			if ctrl&(1<<bit) == 0 {
				out = append(out, data[i])
				dict[w] = data[i]
				w = (w + 1) % len(dict)
				i++
				continue
			}
			if i+1 >= len(data) {
				return out, nil
			}
			ref := int(data[i])<<8 | int(data[i+1])
			i += 2
			off, n := ref>>4, ref&0xF+2
			if off == w { // the end
				return out, nil
			}
			for k := 0; k < n; k++ {
				c := dict[(off+k)%len(dict)]
				out = append(out, c)
				dict[w] = c
				w = (w + 1) % len(dict)
			}
		}
	}
	return out, nil
}

// rtfSkip are the destinations that hold no text of the letter.
var rtfSkip = map[string]bool{"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true, "pict": true,
	"header": true, "footer": true, "object": true, "listtable": true, "listoverridetable": true, "rsidtbl": true,
	"generator": true, "xmlnstbl": true, "themedata": true, "colorschememapping": true, "latentstyles": true, "datastore": true}

// rtfHTML is the HTML an RTF of «\fromhtml1» keeps ("" — the RTF is no HTML): the text of the «\*\htmltag» groups
// and the text outside «\htmlrtf … \htmlrtf0», the RTF's own words for the rest.
func rtfHTML(rtf []byte) string {
	if !bytes.Contains(rtf[:min(len(rtf), 1024)], []byte(`\fromhtml`)) {
		return ""
	}
	type state struct {
		skip, tag, rtf bool
		uc             int
	}
	st := state{uc: 1}
	var stack []state
	var out strings.Builder
	var raw []byte // bytes of the code page, not yet decoded
	cpg := "windows-1252"
	flush := func() {
		if len(raw) == 0 {
			return
		}
		if r, err := charset.Reader(cpg, bytes.NewReader(raw)); err == nil {
			if b, err := io.ReadAll(r); err == nil {
				out.Write(b)
				raw = raw[:0]
				return
			}
		}
		out.Write(raw)
		raw = raw[:0]
	}
	emit := func() bool { return !st.skip && (st.tag || !st.rtf) }
	skipChars := 0 // the characters after \uN that stand for it
	first := false // the group has just started: its first word may make it a destination
	for i := 0; i < len(rtf); {
		c := rtf[i]
		switch c {
		case '{':
			stack = append(stack, st)
			first = true
			i++
			continue
		case '}':
			if len(stack) > 0 {
				st, stack = stack[len(stack)-1], stack[:len(stack)-1]
			}
			first = false
			i++
			continue
		case '\r', '\n':
			i++
			continue
		case '\\':
		default:
			if skipChars > 0 {
				skipChars--
			} else if emit() {
				raw = append(raw, c)
			}
			first = false
			i++
			continue
		}
		// a control word or symbol
		i++
		if i >= len(rtf) {
			break
		}
		c = rtf[i]
		if !isLetter(c) {
			i++
			switch c {
			case '\'':
				if i+2 <= len(rtf) {
					if v, err := strconv.ParseUint(string(rtf[i:i+2]), 16, 8); err == nil {
						if skipChars > 0 {
							skipChars--
						} else if emit() {
							raw = append(raw, byte(v))
						}
					}
					i += 2
				}
			case '*':
				// «\*\word»: a destination the reader may not know — skipped unless it is «htmltag»
				j := i
				for j < len(rtf) && (rtf[j] == ' ' || rtf[j] == '\r' || rtf[j] == '\n') {
					j++
				}
				if j+1 < len(rtf) && rtf[j] == '\\' && bytes.HasPrefix(rtf[j+1:], []byte("htmltag")) {
					st.tag, st.skip = true, false
				} else {
					st.skip = true
				}
			case '{', '}', '\\':
				if emit() {
					raw = append(raw, c)
				}
			case '~':
				if emit() {
					flush()
					out.WriteRune(0xA0) // a no-break space
				}
			}
			first = false
			continue
		}
		j := i
		for j < len(rtf) && isLetter(rtf[j]) {
			j++
		}
		word := string(rtf[i:j])
		k := j
		if k < len(rtf) && (rtf[k] == '-' || isDigit(rtf[k])) {
			k++
			for k < len(rtf) && isDigit(rtf[k]) {
				k++
			}
		}
		param, hasParam := 0, k > j
		if hasParam {
			param, _ = strconv.Atoi(string(rtf[j:k]))
		}
		if k < len(rtf) && rtf[k] == ' ' {
			k++
		}
		i = k
		if first && rtfSkip[word] {
			st.skip = true
		}
		first = false
		switch word {
		case "htmltag":
			st.tag = true
		case "mhtmltag":
			st.skip = true
		case "htmlrtf":
			st.rtf = !hasParam || param != 0
		case "ansicpg":
			flush()
			cpg = "windows-" + strconv.Itoa(param)
		case "uc":
			st.uc = param
		case "u":
			if emit() {
				flush()
				if param < 0 {
					param += 65536
				}
				if r := rune(param); utf8.ValidRune(r) {
					out.WriteRune(r)
				}
			}
			skipChars = st.uc
		case "par", "line":
			if emit() {
				raw = append(raw, '\r', '\n')
			}
		case "tab":
			if emit() {
				raw = append(raw, '\t')
			}
		}
	}
	flush()
	return out.String()
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
