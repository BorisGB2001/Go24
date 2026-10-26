package pst

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestDump prints a .pst — the file of PST_DUMP — to compare a file of Outlook with one of the writer.
func TestDump(t *testing.T) {
	name := os.Getenv("PST_DUMP")
	if name == "" {
		t.Skip("PST_DUMP is not set")
	}
	fh, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	f, err := Open(fh)
	if err != nil {
		t.Fatal(err)
	}
	h := make([]byte, headerSize)
	fh.ReadAt(h, 0)
	fmt.Printf("wVer %d wVerClient %d crypt %d dwUnique %#x bidNextP %#x bidNextB %#x\n", le.Uint16(h[10:]), le.Uint16(h[12:]),
		h[513], le.Uint32(h[40:]), le.Uint64(h[32:]), le.Uint64(h[516:]))
	fmt.Printf("root: eof %#x amapLast %#x amapFree %#x pmapFree %#x valid %d\n", le.Uint64(h[184:]), le.Uint64(h[192:]),
		le.Uint64(h[200:]), le.Uint64(h[208:]), h[180+68])
	for i := 0; i < 32; i++ {
		if v := le.Uint32(h[44+4*i:]); v != 0x400 {
			fmt.Printf("  rgnid[%#x] = %#x\n", i, v)
		}
	}
	for _, nid := range f.NIDs() {
		n, err := f.Node(nid)
		if err != nil {
			fmt.Printf("%#08x: %v\n", nid, err)
			continue
		}
		e := f.nodes[nid]
		fmt.Printf("== node %#x type %#x parent %#x data %d bytes in %d blocks (bid %#x) sub %v\n", nid, nidType(nid), f.Parent(nid),
			len(n.Data()), len(n.data), e.data, hexs(n.SubNIDs()))
		dumpNode(n, "  ")
	}
}

func dumpNode(n *Node, pad string) {
	if len(n.data) == 0 || len(n.data[0]) < 4 || n.data[0][2] != hnSig {
		if d := n.Data(); len(d) > 0 {
			fmt.Printf("%sraw % x\n", pad, d[:min(len(d), 64)])
		}
		return
	}
	switch n.data[0][3] {
	case bTypePC:
		p, err := n.Props()
		if err != nil {
			fmt.Printf("%sPC: %v\n", pad, err)
			return
		}
		fmt.Printf("%sPC fill %08x\n", pad, le.Uint32(n.data[0][8:]))
		printProps(p, pad+"  ")
	case bTypeTC:
		tb, err := n.Table()
		if err != nil {
			fmt.Printf("%sTC: %v\n", pad, err)
			return
		}
		root, _ := n.heap(bTypeTC)
		hd, _ := n.item(root)
		fmt.Printf("%sTC rgib %d %d %d %d, %d columns, %d rows\n", pad, le.Uint16(hd[2:]), le.Uint16(hd[4:]), le.Uint16(hd[6:]),
			le.Uint16(hd[8:]), len(tb.Columns), len(tb.Rows))
		for i := 0; i < int(hd[1]); i++ {
			d := hd[22+8*i:]
			fmt.Printf("%s  col %08x ib %d cb %d bit %d\n", pad, le.Uint32(d), le.Uint16(d[4:]), d[6], d[7])
		}
		for i, r := range tb.Rows {
			fmt.Printf("%s  row %d id %#x\n", pad, i, tb.RowIDs[i])
			printProps(r, pad+"    ")
		}
	default:
		fmt.Printf("%sheap of %#x\n", pad, n.data[0][3])
	}
	for _, s := range n.SubNIDs() {
		if nidType(s) == nidTypeLTP {
			continue
		}
		sn, err := n.Sub(s)
		if err != nil {
			fmt.Printf("%ssub %#x: %v\n", pad, s, err)
			continue
		}
		fmt.Printf("%s-- sub %#x (%d bytes) sub %v\n", pad, s, len(sn.Data()), hexs(sn.SubNIDs()))
		dumpNode(sn, pad+"  ")
	}
}

func printProps(p Props, pad string) {
	tags := make([]uint32, 0, len(p))
	for k := range p {
		tags = append(tags, k)
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i] < tags[j] })
	for _, tag := range tags {
		v := p[tag]
		var s string
		switch uint16(tag) {
		case ptString:
			s = fmt.Sprintf("%q", utf16String(v))
			if len(s) > 100 {
				s = s[:100] + "…"
			}
		case ptInt32, ptInt16, ptBoolean, ptInt64:
			s = fmt.Sprintf("%d", p.Int(tag))
		default:
			s = fmt.Sprintf("% x", v[:min(len(v), 48)])
			if len(v) > 48 {
				s += fmt.Sprintf(" … (%d)", len(v))
			}
		}
		fmt.Printf("%s%08x %s\n", pad, tag, s)
	}
}

func hexs(n []uint32) string {
	var b strings.Builder
	for i, x := range n {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%#x", x)
	}
	return "[" + b.String() + "]"
}
