package mail

import (
	"strings"
	"testing"
)

func TestRTFDecompress(t *testing.T) {
	if len(rtfPrebuf) != 207 {
		t.Fatalf("prebuf %d bytes", len(rtfPrebuf))
	}
	// the example of [MS-OXRTFCP] 3.1.3; «MELA» is kept as it is
	for _, c := range []struct {
		in   []byte
		want string
	}{
		{[]byte{0x1a, 0x00, 0x00, 0x00, 0x1c, 0x00, 0x00, 0x00, 0x4c, 0x5a, 0x46, 0x75, 0xe2, 0xd4, 0x4b, 0x51, 0x41, 0x00, 0x04, 0x20,
			0x57, 0x58, 0x59, 0x5a, 0x0d, 0x6e, 0x7d, 0x01, 0x0e, 0xb0},
			"{\\rtf1 WXYZWXYZWXYZWXYZWXYZ}"},
		{append([]byte{3, 0, 0, 0, 3, 0, 0, 0, 'M', 'E', 'L', 'A', 0, 0, 0, 0}, "abc"...), "abc"},
	} {
		got, err := rtfDecompress(c.in)
		if err != nil || string(got) != c.want {
			t.Errorf("rtfDecompress = %q, %v; want %q", got, err, c.want)
		}
	}
	if _, err := rtfDecompress([]byte("nothing")); err == nil {
		t.Error("not RTF: no error")
	}
}

func TestRTFHTML(t *testing.T) {
	rtf := `{\rtf1\ansi\ansicpg1251\fromhtml1 \fbidis \deff0{\fonttbl{\f0\fswiss Arial;}}
{\*\htmltag19 <html>}{\*\htmltag50 <body>}\htmlrtf {\htmlrtf0
{\*\htmltag64 <p>}\htmlrtf {\htmlrtf0 \'cf\'f0\'e8\'e2\'e5\'f2, |u1084?|u1080?|u1088?!\htmlrtf\par}\htmlrtf0
{\*\htmltag72 </p>}
{\*\mhtmltag84 <a href="x">}{\*\htmltag84 <a href="https://go24.lan/?a=1&amp;b=2">}link{\*\htmltag92 </a>}
{\*\htmltag58 </body>}{\*\htmltag27 </html>}}`
	got := rtfHTML([]byte(strings.ReplaceAll(rtf, "|u", string(rune(92))+"u")))
	want := `<html><body><p>Привет, мир!</p><a href="https://go24.lan/?a=1&amp;b=2">link</a></body></html>`
	if strings.Join(strings.Fields(got), "") != strings.Join(strings.Fields(want), "") {
		t.Errorf("rtfHTML = %q", got)
	}
	if rtfHTML([]byte(`{\rtf1\ansi plain}`)) != "" {
		t.Error("RTF without HTML gave HTML")
	}
}
