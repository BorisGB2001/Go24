package pst

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf16"
)

// ErrNotPST: the file is not a Unicode .pst.
var ErrNotPST = errors.New("not a Unicode PST file")

// File is a .pst open for reading: its node and block B-trees are read whole when it is opened.
type File struct {
	r      io.ReaderAt
	crypt  byte
	nodes  map[uint32]nbtEntry
	blocks map[uint64]bbtEntry
	order  []uint32 // the NIDs of the NBT in its order
}

type nbtEntry struct {
	data, sub uint64
	parent    uint32
}

type bbtEntry struct {
	ib   uint64
	cb   uint16
	refs uint16
}

// Open reads the header and the B-trees of the file.
func Open(r io.ReaderAt) (*File, error) {
	h := make([]byte, headerSize)
	if _, err := r.ReadAt(h, 0); err != nil {
		return nil, ErrNotPST
	}
	if string(h[:4]) != "!BDN" || h[8] != 0x53 || h[9] != 0x4D || le.Uint16(h[10:]) < 23 {
		return nil, ErrNotPST
	}
	if crc(h[8:8+471]) != le.Uint32(h[4:]) {
		return nil, fmt.Errorf("pst: header CRC")
	}
	f := &File{r: r, crypt: h[513], nodes: map[uint32]nbtEntry{}, blocks: map[uint64]bbtEntry{}}
	root := h[180:]
	if err := f.walk(le.Uint64(root[60:]), ptypeBBT, 0); err != nil {
		return nil, err
	}
	if err := f.walk(le.Uint64(root[44:]), ptypeNBT, 0); err != nil {
		return nil, err
	}
	return f, nil
}

// page reads a page and checks its trailer.
func (f *File) page(ib uint64, ptype byte) ([]byte, error) {
	p := make([]byte, pageSize)
	if _, err := f.r.ReadAt(p, int64(ib)); err != nil {
		return nil, fmt.Errorf("pst: page at %#x: %w", ib, err)
	}
	if p[496] != ptype || p[497] != ptype {
		return nil, fmt.Errorf("pst: page at %#x: type %#x, not %#x", ib, p[496], ptype)
	}
	if crc(p[:496]) != le.Uint32(p[500:]) {
		return nil, fmt.Errorf("pst: page at %#x: CRC", ib)
	}
	return p, nil
}

// walk reads a B-tree page and its children.
func (f *File) walk(ib uint64, ptype byte, depth int) error {
	if depth > 8 {
		return fmt.Errorf("pst: B-tree too deep")
	}
	p, err := f.page(ib, ptype)
	if err != nil {
		return err
	}
	n, size, level := int(p[488]), int(p[490]), p[491]
	if n*size > 488 {
		return fmt.Errorf("pst: page at %#x: %d entries", ib, n)
	}
	for i := 0; i < n; i++ {
		e := p[i*size:]
		switch {
		case level > 0:
			if err := f.walk(le.Uint64(e[16:]), ptype, depth+1); err != nil {
				return err
			}
		case ptype == ptypeBBT:
			f.blocks[le.Uint64(e)&^1] = bbtEntry{ib: le.Uint64(e[8:]), cb: le.Uint16(e[16:]), refs: le.Uint16(e[18:])}
		default:
			nid := uint32(le.Uint64(e))
			f.nodes[nid] = nbtEntry{data: le.Uint64(e[8:]), sub: le.Uint64(e[16:]), parent: le.Uint32(e[24:])}
			f.order = append(f.order, nid)
		}
	}
	return nil
}

// block is the data of a block (decoded, when the file is encoded and the block is not internal).
func (f *File) block(bid uint64) ([]byte, error) {
	e, ok := f.blocks[bid&^1]
	if !ok {
		return nil, fmt.Errorf("pst: no block %#x", bid)
	}
	b := make([]byte, e.cb)
	if _, err := f.r.ReadAt(b, int64(e.ib)); err != nil {
		return nil, fmt.Errorf("pst: block %#x: %w", bid, err)
	}
	t := make([]byte, blockTrailer)
	if _, err := f.r.ReadAt(t, int64(e.ib)+int64(blockSize(int(e.cb)))-blockTrailer); err != nil {
		return nil, fmt.Errorf("pst: block %#x: %w", bid, err)
	}
	if le.Uint16(t) != e.cb || le.Uint64(t[8:])&^1 != bid&^1 || le.Uint32(t[4:]) != crc(b) {
		return nil, fmt.Errorf("pst: block %#x: bad trailer", bid)
	}
	if bid&2 == 0 && f.crypt == 1 {
		unpermute(b)
	}
	return b, nil
}

// blockSize is the size of a block for cb bytes of data: with the trailer, in 64-byte units.
func blockSize(cb int) int { return (cb + blockTrailer + 63) &^ 63 }

// dataTree is the data blocks of a node's data: one block, or those of an XBLOCK or an XXBLOCK.
func (f *File) dataTree(bid uint64) ([][]byte, error) {
	if bid == 0 {
		return nil, nil
	}
	b, err := f.block(bid)
	if err != nil {
		return nil, err
	}
	if bid&2 == 0 {
		return [][]byte{b}, nil
	}
	if len(b) < 8 || b[0] != 1 || b[1] < 1 || b[1] > 2 {
		return nil, fmt.Errorf("pst: block %#x: not a data tree", bid)
	}
	n := int(le.Uint16(b[2:]))
	if 8+8*n > len(b) {
		return nil, fmt.Errorf("pst: block %#x: %d entries", bid, n)
	}
	var out [][]byte
	for i := 0; i < n; i++ {
		kid := le.Uint64(b[8+8*i:])
		if b[1] == 2 {
			sub, err := f.dataTree(kid)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		d, err := f.block(kid)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

type slEntry struct{ data, sub uint64 }

// subnodes reads a subnode B-tree (SLBLOCKs under an SIBLOCK).
func (f *File) subnodes(bid uint64, into map[uint32]slEntry) error {
	if bid == 0 {
		return nil
	}
	b, err := f.block(bid)
	if err != nil {
		return err
	}
	if len(b) < 8 || b[0] != 2 {
		return fmt.Errorf("pst: block %#x: not a subnode block", bid)
	}
	n := int(le.Uint16(b[2:]))
	if b[1] == 0 {
		if 8+24*n > len(b) {
			return fmt.Errorf("pst: block %#x: %d entries", bid, n)
		}
		for i := 0; i < n; i++ {
			e := b[8+24*i:]
			into[uint32(le.Uint64(e))] = slEntry{data: le.Uint64(e[8:]), sub: le.Uint64(e[16:])}
		}
		return nil
	}
	if 8+16*n > len(b) {
		return fmt.Errorf("pst: block %#x: %d entries", bid, n)
	}
	for i := 0; i < n; i++ {
		if err := f.subnodes(le.Uint64(b[8+16*i+8:]), into); err != nil {
			return err
		}
	}
	return nil
}

// NIDs are the nodes of the file, in the order of the NBT.
func (f *File) NIDs() []uint32 { return f.order }

// Parent is the parent folder of a node of the NBT (0 — none).
func (f *File) Parent(nid uint32) uint32 { return f.nodes[nid].parent }

// Node is a node read: its data blocks and its subnodes.
type Node struct {
	f    *File
	NID  uint32
	data [][]byte
	sub  map[uint32]slEntry
}

// Node reads a node of the NBT.
func (f *File) Node(nid uint32) (*Node, error) {
	e, ok := f.nodes[nid]
	if !ok {
		return nil, fmt.Errorf("pst: no node %#x", nid)
	}
	return f.node(nid, e.data, e.sub)
}

func (f *File) node(nid uint32, data, sub uint64) (*Node, error) {
	d, err := f.dataTree(data)
	if err != nil {
		return nil, err
	}
	n := &Node{f: f, NID: nid, data: d, sub: map[uint32]slEntry{}}
	return n, f.subnodes(sub, n.sub)
}

// Sub reads a subnode of the node.
func (n *Node) Sub(nid uint32) (*Node, error) {
	e, ok := n.sub[nid]
	if !ok {
		return nil, fmt.Errorf("pst: node %#x: no subnode %#x", n.NID, nid)
	}
	return n.f.node(nid, e.data, e.sub)
}

// SubNIDs are the subnodes of the node, in order.
func (n *Node) SubNIDs() []uint32 {
	out := make([]uint32, 0, len(n.sub))
	for k := range n.sub {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Data is the node's data stream.
func (n *Node) Data() []byte {
	var out []byte
	for _, b := range n.data {
		out = append(out, b...)
	}
	return out
}

// heap checks the node is a heap of that client signature and tells its user root.
func (n *Node) heap(client byte) (uint32, error) {
	if len(n.data) == 0 || len(n.data[0]) < 12 || n.data[0][2] != hnSig {
		return 0, fmt.Errorf("pst: node %#x: not a heap", n.NID)
	}
	if n.data[0][3] != client {
		return 0, fmt.Errorf("pst: node %#x: heap of %#x, not %#x", n.NID, n.data[0][3], client)
	}
	return le.Uint32(n.data[0][4:]), nil
}

// item is a heap item by its HID.
func (n *Node) item(hid uint32) ([]byte, error) {
	if hid == 0 {
		return nil, nil
	}
	blk, idx := int(hid>>16), int(hid>>5&0x7FF)
	if hid&0x1F != 0 || blk >= len(n.data) || idx == 0 {
		return nil, fmt.Errorf("pst: node %#x: bad HID %#x", n.NID, hid)
	}
	b := n.data[blk]
	if len(b) < 2 {
		return nil, fmt.Errorf("pst: node %#x: bad heap page", n.NID)
	}
	pm := int(le.Uint16(b))
	if pm+4 > len(b) {
		return nil, fmt.Errorf("pst: node %#x: bad page map", n.NID)
	}
	count := int(le.Uint16(b[pm:]))
	if idx > count || pm+4+2*count+2 > len(b) {
		return nil, fmt.Errorf("pst: node %#x: no HID %#x", n.NID, hid)
	}
	from, to := int(le.Uint16(b[pm+2+2*idx:])), int(le.Uint16(b[pm+4+2*idx:]))
	if from > to || to > len(b) {
		return nil, fmt.Errorf("pst: node %#x: bad item %#x", n.NID, hid)
	}
	return b[from:to], nil
}

// hnid is a value kept by an HNID: in the heap, or in a subnode.
func (n *Node) hnid(v uint32) ([]byte, error) {
	if v == 0 {
		return nil, nil
	}
	if nidType(v) == nidTypeHID {
		return n.item(v)
	}
	s, err := n.Sub(v)
	if err != nil {
		return nil, err
	}
	return s.Data(), nil
}

// bth reads the records of a BTH whose header is the item hid.
func (n *Node) bth(hid uint32, key, ent int) ([][]byte, error) {
	h, err := n.item(hid)
	if err != nil {
		return nil, err
	}
	if len(h) < 8 || h[0] != bTypeBTH || int(h[1]) != key || int(h[2]) != ent {
		return nil, fmt.Errorf("pst: node %#x: bad BTH header", n.NID)
	}
	var out [][]byte
	var read func(hid uint32, level int) error
	read = func(hid uint32, level int) error {
		b, err := n.item(hid)
		if err != nil {
			return err
		}
		size := key + ent
		if level > 0 {
			size = key + 4
		}
		for i := 0; i+size <= len(b); i += size {
			if level > 0 {
				if err := read(le.Uint32(b[i+key:]), level-1); err != nil {
					return err
				}
				continue
			}
			out = append(out, b[i:i+size])
		}
		return nil
	}
	if root := le.Uint32(h[4:]); root != 0 {
		if err := read(root, int(h[3])); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Props are the properties of an object by their tags (ID in the upper 16 bits, type in the lower): numbers and
// times as their little-endian bytes (a boolean in one byte), strings in UTF-16LE.
type Props map[uint32][]byte

// Props reads the node's property context.
func (n *Node) Props() (Props, error) {
	root, err := n.heap(bTypePC)
	if err != nil {
		return nil, err
	}
	recs, err := n.bth(root, 2, 6)
	if err != nil {
		return nil, err
	}
	p := Props{}
	for _, r := range recs {
		id, typ, v := le.Uint16(r), le.Uint16(r[2:]), le.Uint32(r[4:])
		tag := uint32(id)<<16 | uint32(typ)
		if size := fixedSize(typ); size > 0 && size <= 4 {
			b := make([]byte, 4)
			le.PutUint32(b, v)
			p[tag] = b[:size]
			continue
		}
		val, err := n.hnid(v)
		if err != nil {
			return nil, err
		}
		p[tag] = val
	}
	return p, nil
}

// String is a string property (PtypString).
func (p Props) String(tag uint32) string { return utf16String(p[tag]) }

// Subject is the subject without the mark of its prefix's length that Outlook keeps it with.
func (p Props) Subject() string {
	s := []rune(p.String(TagSubject))
	if len(s) >= 2 && s[0] == 1 {
		return string(s[2:])
	}
	return string(s)
}

// Int is a number property (up to 8 bytes).
func (p Props) Int(tag uint32) int64 {
	b := append(append([]byte{}, p[tag]...), make([]byte, 8)...)
	return int64(le.Uint64(b))
}

func utf16String(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = le.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// fixedSize is the size of a property type of a fixed size (0 — a variable one).
func fixedSize(typ uint16) int {
	switch typ {
	case ptBoolean:
		return 1
	case ptInt16:
		return 2
	case ptInt32, ptFloat32, ptError:
		return 4
	case ptFloat64, ptCurrency, ptAppTime, ptInt64, ptTime:
		return 8
	case ptGUID:
		return 16
	}
	return 0
}

// Table is a table context read.
type Table struct {
	Columns []uint32 // tags, in order
	Rows    []Props
	RowIDs  []uint32 // by the row index
}

// Table reads the node's table context.
func (n *Node) Table() (*Table, error) {
	root, err := n.heap(bTypeTC)
	if err != nil {
		return nil, err
	}
	h, err := n.item(root)
	if err != nil {
		return nil, err
	}
	if len(h) < 22 || h[0] != bTypeTC || len(h) < 22+8*int(h[1]) {
		return nil, fmt.Errorf("pst: node %#x: bad TC header", n.NID)
	}
	type col struct {
		tag       uint32
		ib, cb, i int
	}
	cols := make([]col, h[1])
	t := &Table{}
	for i := range cols {
		d := h[22+8*i:]
		cols[i] = col{tag: le.Uint32(d), ib: int(le.Uint16(d[4:])), cb: int(d[6]), i: int(d[7])}
		t.Columns = append(t.Columns, cols[i].tag)
	}
	rowSize := int(le.Uint16(h[8:]))
	ids, err := n.bth(le.Uint32(h[10:]), 4, 4)
	if err != nil {
		return nil, err
	}
	t.RowIDs = make([]uint32, len(ids))
	for _, r := range ids {
		if i := int(le.Uint32(r[4:])); i < len(ids) {
			t.RowIDs[i] = le.Uint32(r)
		}
	}
	var blocks [][]byte
	if hn := le.Uint32(h[14:]); hn != 0 {
		if nidType(hn) == nidTypeHID {
			b, err := n.item(hn)
			if err != nil {
				return nil, err
			}
			blocks = [][]byte{b}
		} else {
			s, err := n.Sub(hn)
			if err != nil {
				return nil, err
			}
			blocks = s.data
		}
	}
	for _, b := range blocks {
		for o := 0; rowSize > 0 && o+rowSize <= len(b); o += rowSize {
			row, ceb := b[o:o+rowSize], b[o+int(le.Uint16(h[6:])):o+rowSize]
			p := Props{}
			for _, c := range cols {
				if c.i/8 >= len(ceb) || ceb[c.i/8]&(1<<(7-c.i%8)) == 0 || c.ib+c.cb > len(row) {
					continue
				}
				cell := row[c.ib : c.ib+c.cb]
				typ := uint16(c.tag)
				if size := fixedSize(typ); size > 0 && size <= 8 {
					p[c.tag] = append([]byte{}, cell[:size]...)
					continue
				}
				val, err := n.hnid(le.Uint32(cell))
				if err != nil {
					return nil, err
				}
				p[c.tag] = val
			}
			t.Rows = append(t.Rows, p)
		}
	}
	if len(t.Rows) > len(ids) {
		t.Rows = t.Rows[:len(ids)]
	}
	return t, nil
}

// Folders are the folders of the file by their paths from the root («Top of Personal Folders/Входящие»), each with
// its letters' nodes in the order of its contents table.
func (f *File) Folders() (map[string][]uint32, error) {
	tree, err := f.Tree()
	out := map[string][]uint32{}
	for _, t := range tree {
		out[strings.Join(t.Path, "/")] = t.Letters
	}
	return out, err
}

// FolderOf is a folder of the file: the names of its path from the root and its letters' nodes.
type FolderOf struct {
	Path    []string
	Letters []uint32
}

// Tree is the folders of the file in the order of the tree: each folder before its subfolders, those in the order of
// its hierarchy table.
func (f *File) Tree() ([]FolderOf, error) {
	var out []FolderOf
	var walk func(nid uint32, path []string) error
	walk = func(nid uint32, path []string) error {
		if len(path) > 50 {
			return fmt.Errorf("pst: folders too deep")
		}
		if len(path) > 0 {
			ct, err := f.Node(makeNID(nidTypeContentsTable, nid>>5))
			if err != nil {
				return err
			}
			t, err := ct.Table()
			if err != nil {
				return err
			}
			out = append(out, FolderOf{Path: path, Letters: t.RowIDs})
		}
		ht, err := f.Node(makeNID(nidTypeHierarchyTable, nid>>5))
		if err != nil {
			return err
		}
		t, err := ht.Table()
		if err != nil {
			return err
		}
		for i, r := range t.Rows {
			if id := t.RowIDs[i]; nidType(id) == nidTypeNormalFolder {
				if err := walk(id, append(path[:len(path):len(path)], r.String(TagDisplayName))); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return out, walk(nidRootFolder, nil)
}

// Letter is a letter read: its properties, its recipients' and its attachments' (with their data).
type Letter struct {
	Props       Props
	Recipients  []Props
	Attachments []Props
}

// Letter reads a letter of its node.
func (f *File) Letter(nid uint32) (*Letter, error) {
	n, err := f.Node(nid)
	if err != nil {
		return nil, err
	}
	l := &Letter{}
	if l.Props, err = n.Props(); err != nil {
		return nil, err
	}
	for _, sub := range []uint32{nidRecipientTableTemplate, nidAttachmentTableTemplate} {
		if _, ok := n.sub[sub]; !ok {
			continue
		}
		tn, err := n.Sub(sub)
		if err != nil {
			return nil, err
		}
		t, err := tn.Table()
		if err != nil {
			return nil, err
		}
		if sub == nidRecipientTableTemplate {
			l.Recipients = t.Rows
			continue
		}
		for _, id := range t.RowIDs {
			an, err := n.Sub(id)
			if err != nil {
				return nil, err
			}
			p, err := an.Props()
			if err != nil {
				return nil, err
			}
			l.Attachments = append(l.Attachments, p)
		}
	}
	return l, nil
}
