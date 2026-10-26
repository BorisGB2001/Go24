package pst

import (
	"fmt"
	"sort"
)

// heap builds a heap-on-node: items in pages, each page a data block of the node.
type heap struct {
	client byte
	root   uint32
	pages  [][][]byte // the items of each page
	sizes  []int      // the bytes of each page's items
}

const heapItemsMax = 2047 // the items a page keeps: the 11 bits of hidIndex

func newHeap(client byte) *heap { return &heap{client: client} }

// pageHead is the size of a page's header: HNHDR on the first, HNBITMAPHDR on the 8th and every 128th after it.
func pageHead(i int) int {
	switch {
	case i == 0:
		return 12
	case i >= 8 && (i-8)%128 == 0:
		return 66
	}
	return 2
}

// pageBytes is the size of a page with n items of size bytes.
func pageBytes(i, n, size int) int { return (pageHead(i)+size+1)&^1 + 4 + 2*(n+1) }

// alloc puts an item into the heap and tells its HID.
func (h *heap) alloc(b []byte) (uint32, error) {
	if len(b) > heapItemMax {
		return 0, fmt.Errorf("pst: heap item of %d bytes", len(b))
	}
	last := len(h.pages) - 1
	if last < 0 || len(h.pages[last]) >= heapItemsMax || pageBytes(last, len(h.pages[last])+1, h.sizes[last]+len(b)) > blockData {
		if len(h.pages) >= 1<<16 {
			return 0, fmt.Errorf("pst: heap of %d pages", len(h.pages))
		}
		h.pages = append(h.pages, nil)
		h.sizes = append(h.sizes, 0)
		last++
	}
	h.pages[last] = append(h.pages[last], b)
	h.sizes[last] += len(b)
	return uint32(len(h.pages[last]))<<5 | uint32(last)<<16, nil
}

// fillLevel is the fill level of a page by its free bytes.
func fillLevel(free int) byte {
	for i, least := range []int{3584, 2560, 2048, 1792, 1536, 1280, 1024, 768, 512, 256, 128, 64, 32, 16, 8} {
		if free >= least {
			return byte(i)
		}
	}
	return 15
}

// blocks is the heap's pages as data blocks.
func (h *heap) blocks() [][]byte {
	if len(h.pages) == 0 {
		h.pages, h.sizes = [][][]byte{nil}, []int{0}
	}
	levels := make([]byte, len(h.pages))
	out := make([][]byte, len(h.pages))
	for i, items := range h.pages {
		size := pageBytes(i, len(items), h.sizes[i])
		levels[i] = fillLevel(blockData - size)
		b := make([]byte, size)
		o := pageHead(i)
		offs := make([]int, 0, len(items)+1)
		for _, it := range items {
			offs = append(offs, o)
			o += copy(b[o:], it)
		}
		offs = append(offs, o)
		pm := (o + 1) &^ 1
		le.PutUint16(b, uint16(pm))
		le.PutUint16(b[pm:], uint16(len(items)))
		for j, x := range offs {
			le.PutUint16(b[pm+4+2*j:], uint16(x))
		}
		out[i] = b
	}
	put := func(dst []byte, from, n int) {
		for j := 0; j < n && from+j < len(levels); j++ {
			dst[j/2] |= levels[from+j] << (4 * (j % 2))
		}
	}
	first := out[0]
	first[2], first[3] = hnSig, h.client
	le.PutUint32(first[4:], h.root)
	put(first[8:12], 0, 8)
	for i := 8; i < len(out); i += 128 {
		put(out[i][2:66], i, 128)
	}
	return out
}

// bth puts a B-tree of the records (sorted by their keys) into the heap and tells the HID of its header.
func (h *heap) bth(key, ent int, recs [][]byte) (uint32, error) {
	head := []byte{bTypeBTH, byte(key), byte(ent), 0, 0, 0, 0, 0}
	if len(recs) == 0 {
		return h.alloc(head)
	}
	type idx struct {
		key []byte
		hid uint32
	}
	size := key + ent
	var level []idx
	for start := 0; start < len(recs); start += heapItemMax / size {
		part := recs[start:min(start+heapItemMax/size, len(recs))]
		b := make([]byte, 0, len(part)*size)
		for _, r := range part {
			b = append(b, r...)
		}
		hid, err := h.alloc(b)
		if err != nil {
			return 0, err
		}
		level = append(level, idx{part[0][:key], hid})
	}
	levels := 0
	for ; len(level) > 1; levels++ {
		var up []idx
		per := heapItemMax / (key + 4)
		for start := 0; start < len(level); start += per {
			part := level[start:min(start+per, len(level))]
			b := make([]byte, 0, len(part)*(key+4))
			for _, x := range part {
				b = append(b, x.key...)
				b = le.AppendUint32(b, x.hid)
			}
			hid, err := h.alloc(b)
			if err != nil {
				return 0, err
			}
			up = append(up, idx{part[0].key, hid})
		}
		level = up
	}
	head[3] = byte(levels)
	le.PutUint32(head[4:], level[0].hid)
	return h.alloc(head)
}

// ltpNode is a node of the LTP being made: its heap, and the subnodes its large values go to.
type ltpNode struct {
	d    *ndb
	heap *heap
	subs []subnode
	next uint32 // the index of the next subnode of NID_TYPE_LTP
}

func newLTPNode(d *ndb, client byte) *ltpNode {
	return &ltpNode{d: d, heap: newHeap(client), next: 0x400}
}

// hnid keeps a variable value: in the heap when it fits, otherwise in a subnode of its own.
func (n *ltpNode) hnid(b []byte) (uint32, error) {
	if len(b) <= heapItemMax {
		return n.heap.alloc(b)
	}
	bid, err := n.d.data(b)
	if err != nil {
		return 0, err
	}
	n.next++
	nid := makeNID(nidTypeLTP, n.next)
	n.subs = append(n.subs, subnode{nid: nid, data: bid})
	return nid, nil
}

// write writes the node's heap and subnodes; it tells the BIDs of its data and of its subnode tree.
func (n *ltpNode) write() (data, sub uint64, err error) {
	if data, err = n.d.tree(n.heap.blocks()); err != nil {
		return 0, 0, err
	}
	sub, err = n.d.subnodes(n.subs)
	return data, sub, err
}

// prop is a property to keep: its tag and its value encoded (numbers little-endian, strings UTF-16LE).
type prop struct {
	tag uint32
	val []byte
}

// pc makes a property context of the properties; more subnodes (an attachment table, attachments) can be added.
func (d *ndb) pc(props []prop, more ...subnode) (data, sub uint64, err error) {
	n := newLTPNode(d, bTypePC)
	sort.Slice(props, func(i, j int) bool { return props[i].tag < props[j].tag })
	recs := make([][]byte, 0, len(props))
	for i, p := range props {
		if i > 0 && props[i-1].tag>>16 == p.tag>>16 {
			return 0, 0, fmt.Errorf("pst: property %#x twice", p.tag>>16)
		}
		r := make([]byte, 8)
		le.PutUint16(r, uint16(p.tag>>16))
		le.PutUint16(r[2:], uint16(p.tag))
		if size := fixedSize(uint16(p.tag)); size > 0 && size <= 4 {
			copy(r[4:4+size], p.val)
		} else {
			var v uint32
			if size > 4 {
				v, err = n.heap.alloc(p.val)
			} else {
				v, err = n.hnid(p.val)
			}
			if err != nil {
				return 0, 0, err
			}
			le.PutUint32(r[4:], v)
		}
		recs = append(recs, r)
	}
	if n.heap.root, err = n.heap.bth(2, 6, recs); err != nil {
		return 0, 0, err
	}
	n.subs = append(n.subs, more...)
	return n.write()
}

// column is a column of a table context: its tag, where its cell is in a row, its bit of the cell existence block.
type column struct {
	tag    uint32
	ib, cb int
	bit    int
}

// layout lays out the columns of a table: the row ID and version first, then the cells of 8 and 4 bytes, of 2,
// of 1, the cell existence block last. It tells the ends of the groups (rgib).
func layout(tags []uint32) ([]column, [4]int) {
	cols := make([]column, 0, len(tags)+2)
	cols = append(cols, column{tag: TagLtpRowID, ib: 0, cb: 4, bit: 0}, column{tag: TagLtpRowVer, ib: 4, cb: 4, bit: 1})
	var rest []column
	for _, t := range tags {
		if t == TagLtpRowID || t == TagLtpRowVer {
			continue
		}
		cb := 4 // an HNID
		if size := fixedSize(uint16(t)); size > 0 && size <= 8 {
			cb = size
		}
		rest = append(rest, column{tag: t, cb: cb, bit: 2 + len(rest)})
	}
	o := 8
	var rgib [4]int
	for g, sizes := range [][]int{{8, 4}, {2}, {1}} {
		for _, size := range sizes {
			for i := range rest {
				if rest[i].cb == size {
					rest[i].ib = o
					o += size
				}
			}
		}
		rgib[g] = o
	}
	cols = append(cols, rest...)
	rgib[3] = o + (len(cols)+7)/8
	sort.Slice(cols, func(i, j int) bool { return cols[i].tag < cols[j].tag })
	return cols, rgib
}

// row is a row of a table: its ID and its cells by tag.
type row struct {
	id    uint32
	cells map[uint32][]byte
}

// tcBuilder makes a table context row by row: the cells go into the row matrix and the heap as rows come.
type tcBuilder struct {
	n      *ltpNode
	cols   []column
	rgib   [4]int
	matrix []byte
	ids    [][]byte
}

func (d *ndb) newTC(tags []uint32) *tcBuilder {
	t := &tcBuilder{n: newLTPNode(d, bTypeTC)}
	t.cols, t.rgib = layout(tags)
	return t
}

// tc makes a table context of those columns and rows.
func (d *ndb) tc(tags []uint32, rows []row) (data, sub uint64, err error) {
	t := d.newTC(tags)
	for _, r := range rows {
		if err := t.add(r); err != nil {
			return 0, 0, err
		}
	}
	return t.write()
}

// add puts a row into the table; a row without its version gets its number.
func (t *tcBuilder) add(r row) (err error) {
	i := len(t.ids)
	b := make([]byte, t.rgib[3])
	ceb := b[t.rgib[2]:]
	for _, c := range t.cols {
		v, ok := r.cells[c.tag]
		switch c.tag {
		case TagLtpRowID:
			v, ok = le.AppendUint32(nil, r.id), true
		case TagLtpRowVer:
			if !ok {
				v, ok = le.AppendUint32(nil, uint32(i+1)), true
			}
		}
		if !ok {
			continue
		}
		ceb[c.bit/8] |= 0x80 >> (c.bit % 8)
		if size := fixedSize(uint16(c.tag)); size > 0 && size <= 8 {
			copy(b[c.ib:c.ib+c.cb], v)
			continue
		}
		var h uint32
		if fixedSize(uint16(c.tag)) > 8 {
			h, err = t.n.heap.alloc(v)
		} else {
			h, err = t.n.hnid(v)
		}
		if err != nil {
			return err
		}
		le.PutUint32(b[c.ib:], h)
	}
	t.matrix = append(t.matrix, b...)
	t.ids = append(t.ids, le.AppendUint32(le.AppendUint32(nil, r.id), uint32(i)))
	return nil
}

// write writes the table: the row index, the row matrix (in the heap when small, otherwise in a subnode), the header.
func (t *tcBuilder) write() (data, sub uint64, err error) {
	n, size := t.n, t.rgib[3]
	sort.Slice(t.ids, func(i, j int) bool { return le.Uint32(t.ids[i]) < le.Uint32(t.ids[j]) })
	rowIndex, err := n.heap.bth(4, 4, t.ids)
	if err != nil {
		return 0, 0, err
	}
	var hnRows uint32
	rows := len(t.ids)
	switch {
	case rows == 0:
	case len(t.matrix) <= heapItemMax:
		if hnRows, err = n.heap.alloc(t.matrix); err != nil {
			return 0, 0, err
		}
	default: // in a subnode: whole rows in each block, all but the last block full
		per := blockData / size
		var chunks [][]byte
		for start := 0; start < rows; start += per {
			c := t.matrix[start*size : min(start+per, rows)*size]
			if start+per < rows {
				c = append(c[:len(c):len(c)], make([]byte, blockData-len(c))...)
			}
			chunks = append(chunks, c)
		}
		bid, err := n.d.tree(chunks)
		if err != nil {
			return 0, 0, err
		}
		n.next++
		hnRows = makeNID(nidTypeLTP, n.next)
		n.subs = append(n.subs, subnode{nid: hnRows, data: bid})
	}
	head := make([]byte, 22+8*len(t.cols))
	head[0], head[1] = bTypeTC, byte(len(t.cols))
	for i, x := range t.rgib {
		le.PutUint16(head[2+2*i:], uint16(x))
	}
	le.PutUint32(head[10:], rowIndex)
	le.PutUint32(head[14:], hnRows)
	for i, c := range t.cols {
		e := head[22+8*i:]
		le.PutUint32(e, c.tag)
		le.PutUint16(e[4:], uint16(c.ib))
		e[6], e[7] = byte(c.cb), byte(c.bit)
	}
	if n.heap.root, err = n.heap.alloc(head); err != nil {
		return 0, 0, err
	}
	t.matrix, t.ids = nil, nil
	return n.write()
}
