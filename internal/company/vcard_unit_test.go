package company

import (
	"strings"
	"testing"
)

// TestVCardEscapingAndFolding: vcEsc escapes special chars and foldLine wraps lines at 75 octets without breaking UTF-8.
func TestVCardEscapingAndFolding(t *testing.T) {
	// vcEsc
	raw := "Hello\\World; Foo, Bar\r\nNext\nLine\rCarriage"
	escaped := vcEsc(raw)
	want := `Hello\\World\; Foo\, Bar\nNext\nLineCarriage`
	if escaped != want {
		t.Errorf("vcEsc(%q) = %q, want %q", raw, escaped, want)
	}

	// foldLine short
	short := "Short line under 75 octets"
	if got := foldLine(short); got != short {
		t.Errorf("foldLine(short) = %q, want %q", got, short)
	}

	// foldLine exact 75 octets
	exact75 := strings.Repeat("a", 75)
	if got := foldLine(exact75); got != exact75 {
		t.Errorf("foldLine(exact75) modified string: %q", got)
	}

	// foldLine 76 octets
	line76 := strings.Repeat("a", 76)
	folded76 := foldLine(line76)
	want76 := strings.Repeat("a", 75) + "\r\n a"
	if folded76 != want76 {
		t.Errorf("foldLine(76) = %q, want %q", folded76, want76)
	}

	// foldLine with Cyrillic characters near 75-byte boundary (each Cyrillic char is 2 bytes in UTF-8)
	// 37 Cyrillic 'я' is 74 bytes. Adding one more 'я' brings it to 76 bytes, so the 38th 'я' must wrap to the next line.
	cyrillic74 := strings.Repeat("я", 37) // 74 bytes
	cyrillic76 := cyrillic74 + "я"        // 76 bytes
	foldedCyrillic := foldLine(cyrillic76)
	wantCyrillic := cyrillic74 + "\r\n я"
	if foldedCyrillic != wantCyrillic {
		t.Errorf("foldLine(Cyrillic) = %q, want %q", foldedCyrillic, wantCyrillic)
	}

	// CardsCTag
	tagNil := CardsCTag(nil)
	if tagNil == "" {
		t.Errorf("CardsCTag(nil) should return valid hex hash")
	}
	tag1 := CardsCTag([]string{"etag1", "etag2"})
	tag2 := CardsCTag([]string{"etag1", "etag2"})
	if tag1 != tag2 || tag1 == "" {
		t.Errorf("CardsCTag deterministic hash failed: %q vs %q", tag1, tag2)
	}
	tagDiff := CardsCTag([]string{"etag2", "etag1"})
	if tag1 == tagDiff {
		t.Errorf("CardsCTag should differ for different ordering or contents")
	}
}
