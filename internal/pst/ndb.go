package pst

import (
	"fmt"
	"io"
	"sort"
)

// Output is where a .pst is written: blocks go at their offsets, the header last.
type Output interface {
	io.WriterAt
	Truncate(size int64) error
}

// ndb writes the node database: the blocks are written as they come, the B-trees and the maps when it is closed.
type ndb struct {
	out     Output
	end     int64    // the allocation goes on from here
	maps    [][]byte // the allocation bits of each AMap's section
	nextBID uint64
	nextPID uint64
	bbt     []bbtRec
	refs    map[uint64]uint16 // the blocks referenced more than once: their counts
	nbt     []nbtRec
	rgnid   [32]uint32
}

type bbtRec struct {
	bid, ib uint64
	cb      uint16
}

type nbtRec struct {
	nid       uint32
	data, sub uint64
	parent    uint32
}

// subnode is an entry of a node's subnode tree.
type subnode struct {
	nid       uint32
	data, sub uint64
}

func newNDB(out Output) *ndb {
	d := &ndb{out: out, end: amapFirst, nextBID: 4, nextPID: 1, refs: map[uint64]uint16{}}
	for i := range d.rgnid {
		d.rgnid[i] = 0x400
	}
	d.rgnid[nidTypeSearchFolder] = 0x4000
	d.rgnid[nidTypeNormalMessage] = 0x10000
	d.rgnid[nidTypeAssocMessage] = 0x8000
	return d
}

// newNID gives the next node ID of the type.
func (d *ndb) newNID(typ uint32) uint32 {
	d.rgnid[typ]++
	return makeNID(typ, d.rgnid[typ])
}

// mapPages are the offsets, within its section, of the map pages an AMap's section begins with.
func mapPages(i int64) []int64 {
	pages := []int64{0}
	if i%pmapEvery == 0 {
		pages = append(pages, pageSize)
		if i >= fmapFirstAt && (i-fmapFirstAt)%fmapEvery == 0 {
			pages = append(pages, 2*pageSize)
		}
		if i >= fpmapFirstAt && (i-fpmapFirstAt)%fpmapEvery == 0 {
			pages = append(pages, 3*pageSize)
		}
	}
	return pages
}

// section is the allocation bits of the i-th AMap's section, made (with its map pages taken) when new.
func (d *ndb) section(i int64) []byte {
	for int64(len(d.maps)) <= i {
		m := make([]byte, 496)
		for _, p := range mapPages(int64(len(d.maps))) {
			m[p/512] = 0xFF
		}
		d.maps = append(d.maps, m)
	}
	return d.maps[i]
}

// alloc takes size bytes aligned to align (64 or 512) from the data sections.
func (d *ndb) alloc(size, align int64) int64 {
	for {
		i := (d.end - amapFirst) / amapCovers
		start := amapFirst + i*amapCovers
		pages := mapPages(i)
		if first := start + pages[len(pages)-1] + pageSize; d.end < first {
			d.end = first
		}
		ib := (d.end + align - 1) &^ (align - 1)
		if ib+size <= start+amapCovers {
			m := d.section(i)
			for o := ib - start; o < ib-start+size; o += 64 {
				m[o/512] |= 0x80 >> (o / 64 % 8)
			}
			d.end = ib + size
			return ib
		}
		d.end = start + amapCovers
	}
}

// block writes a block and tells its ID; an internal one (of a data tree or subnodes) has the bit 2 set.
func (d *ndb) block(data []byte, internal bool) (uint64, error) {
	if len(data) > blockData {
		return 0, fmt.Errorf("pst: block of %d bytes", len(data))
	}
	bid := d.nextBID
	d.nextBID += 4
	if internal {
		bid |= 2
	}
	size := int64(blockSize(len(data)))
	ib := d.alloc(size, 64)
	b := make([]byte, size)
	copy(b, data)
	t := b[size-blockTrailer:]
	le.PutUint16(t, uint16(len(data)))
	le.PutUint16(t[2:], sig(uint64(ib), bid))
	le.PutUint32(t[4:], crc(data))
	le.PutUint64(t[8:], bid)
	if _, err := d.out.WriteAt(b, ib); err != nil {
		return 0, err
	}
	d.bbt = append(d.bbt, bbtRec{bid: bid, ib: uint64(ib), cb: uint16(len(data))})
	return bid, nil
}

// ref counts one more reference to a block written (the BBT and the first referrer are counted already).
func (d *ndb) ref(bid uint64) {
	if d.refs[bid] == 0 {
		d.refs[bid] = 2
	}
	d.refs[bid]++
}

// data writes a node's data stream: none (BID 0), one block, or a data tree of XBLOCKs (and an XXBLOCK over them).
func (d *ndb) data(b []byte) (uint64, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if len(b) <= blockData {
		return d.block(b, false)
	}
	var chunks [][]byte
	for len(b) > 0 {
		n := min(len(b), blockData)
		chunks = append(chunks, b[:n])
		b = b[n:]
	}
	return d.tree(chunks)
}

// tree writes the data blocks as a data tree (they are pages of a heap, or parts of a stream).
func (d *ndb) tree(chunks [][]byte) (uint64, error) {
	if len(chunks) == 1 {
		return d.block(chunks[0], false)
	}
	type xb struct {
		bid  uint64
		size int
	}
	var xs []xb
	for start := 0; start < len(chunks); start += xblockEntries {
		part := chunks[start:min(start+xblockEntries, len(chunks))]
		x := make([]byte, 8+8*len(part))
		total := 0
		for i, c := range part {
			bid, err := d.block(c, false)
			if err != nil {
				return 0, err
			}
			le.PutUint64(x[8+8*i:], bid)
			total += len(c)
		}
		x[0], x[1] = 1, 1
		le.PutUint16(x[2:], uint16(len(part)))
		le.PutUint32(x[4:], uint32(total))
		bid, err := d.block(x, true)
		if err != nil {
			return 0, err
		}
		xs = append(xs, xb{bid, total})
	}
	if len(xs) == 1 {
		return xs[0].bid, nil
	}
	if len(xs) > xblockEntries {
		return 0, fmt.Errorf("pst: data of %d blocks", len(chunks))
	}
	x := make([]byte, 8+8*len(xs))
	total := 0
	for i, e := range xs {
		le.PutUint64(x[8+8*i:], e.bid)
		total += e.size
	}
	x[0], x[1] = 1, 2
	le.PutUint16(x[2:], uint16(len(xs)))
	le.PutUint32(x[4:], uint32(total))
	return d.block(x, true)
}

const (
	slEntries = (blockData - 8) / 24 // the SLENTRYs an SLBLOCK keeps
	siEntries = (blockData - 8) / 16 // the SIENTRYs an SIBLOCK keeps
)

// subnodes writes a subnode tree: SLBLOCKs, and an SIBLOCK over them when one is not enough (BID 0 — none).
func (d *ndb) subnodes(subs []subnode) (uint64, error) {
	if len(subs) == 0 {
		return 0, nil
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].nid < subs[j].nid })
	type si struct {
		nid uint32
		bid uint64
	}
	var leaves []si
	for start := 0; start < len(subs); start += slEntries {
		part := subs[start:min(start+slEntries, len(subs))]
		b := make([]byte, 8+24*len(part))
		b[0], b[1] = 2, 0
		le.PutUint16(b[2:], uint16(len(part)))
		for i, s := range part {
			e := b[8+24*i:]
			le.PutUint64(e, uint64(s.nid))
			le.PutUint64(e[8:], s.data)
			le.PutUint64(e[16:], s.sub)
		}
		bid, err := d.block(b, true)
		if err != nil {
			return 0, err
		}
		leaves = append(leaves, si{part[0].nid, bid})
	}
	if len(leaves) == 1 {
		return leaves[0].bid, nil
	}
	if len(leaves) > siEntries {
		return 0, fmt.Errorf("pst: %d subnodes", len(subs))
	}
	b := make([]byte, 8+16*len(leaves))
	b[0], b[1] = 2, 1
	le.PutUint16(b[2:], uint16(len(leaves)))
	for i, l := range leaves {
		le.PutUint64(b[8+16*i:], uint64(l.nid))
		le.PutUint64(b[8+16*i+8:], l.bid)
	}
	return d.block(b, true)
}

// node puts a node into the NBT.
func (d *ndb) node(nid uint32, data, sub uint64, parent uint32) {
	d.nbt = append(d.nbt, nbtRec{nid: nid, data: data, sub: sub, parent: parent})
}

// page writes a B-tree page; it tells its offset.
func (d *ndb) page(b []byte, ptype byte) (bid uint64, ib int64, err error) {
	bid = d.nextPID
	d.nextPID++
	ib = d.alloc(pageSize, pageSize)
	pageTrailer(b, ptype, sig(uint64(ib), bid), bid)
	_, err = d.out.WriteAt(b, ib)
	return bid, ib, err
}

func pageTrailer(b []byte, ptype byte, wsig uint16, bid uint64) {
	b[496], b[497] = ptype, ptype
	le.PutUint16(b[498:], wsig)
	le.PutUint32(b[500:], crc(b[:496]))
	le.PutUint64(b[504:], bid)
}

type btRef struct {
	key, bid uint64
	ib       int64
}

// btree writes a B-tree of the leaf entries (each its key first) and tells the root page.
func (d *ndb) btree(ptype byte, entries [][]byte, size int) (btRef, error) {
	per := 488 / size
	var level []btRef
	write := func(items [][]byte, cbEnt int, depth byte) (btRef, error) {
		b := make([]byte, pageSize)
		for i, e := range items {
			copy(b[i*cbEnt:], e)
		}
		b[488], b[489], b[490], b[491] = byte(len(items)), byte(488/cbEnt), byte(cbEnt), depth
		var key uint64
		if len(items) > 0 {
			key = le.Uint64(items[0])
		}
		bid, ib, err := d.page(b, ptype)
		return btRef{key, bid, ib}, err
	}
	for start := 0; start < len(entries) || start == 0; start += per {
		r, err := write(entries[start:min(start+per, len(entries))], size, 0)
		if err != nil {
			return btRef{}, err
		}
		level = append(level, r)
		if len(entries) == 0 {
			break
		}
	}
	for depth := byte(1); len(level) > 1; depth++ {
		var up []btRef
		for start := 0; start < len(level); start += 488 / 24 {
			var items [][]byte
			for _, r := range level[start:min(start+488/24, len(level))] {
				e := make([]byte, 24)
				le.PutUint64(e, r.key)
				le.PutUint64(e[8:], r.bid)
				le.PutUint64(e[16:], uint64(r.ib))
				items = append(items, e)
			}
			r, err := write(items, 24, depth)
			if err != nil {
				return btRef{}, err
			}
			up = append(up, r)
		}
		level = up
	}
	return level[0], nil
}

// close writes the B-trees, the maps and the header.
func (d *ndb) close() error {
	sort.Slice(d.nbt, func(i, j int) bool { return d.nbt[i].nid < d.nbt[j].nid })
	nodes := make([][]byte, len(d.nbt))
	for i, n := range d.nbt {
		e := make([]byte, 32)
		le.PutUint64(e, uint64(n.nid))
		le.PutUint64(e[8:], n.data)
		le.PutUint64(e[16:], n.sub)
		le.PutUint32(e[24:], n.parent)
		nodes[i] = e
	}
	blocks := make([][]byte, len(d.bbt))
	for i, b := range d.bbt {
		e := make([]byte, 24)
		le.PutUint64(e, b.bid)
		le.PutUint64(e[8:], b.ib)
		le.PutUint16(e[16:], b.cb)
		refs := d.refs[b.bid]
		if refs == 0 {
			refs = 2
		}
		le.PutUint16(e[18:], refs)
		blocks[i] = e
	}
	// the pages of the B-trees take space of the maps, so the BBT does not change while they are written
	bbt, err := d.btree(ptypeBBT, blocks, 24)
	if err != nil {
		return err
	}
	nbt, err := d.btree(ptypeNBT, nodes, 32)
	if err != nil {
		return err
	}
	// the file is as long as the sections its AMaps map
	sections := (d.end - amapFirst + amapCovers - 1) / amapCovers
	d.section(sections - 1)
	eof := amapFirst + sections*amapCovers
	if err := d.out.Truncate(eof); err != nil {
		return err
	}
	var free int64
	fmax := make([]byte, len(d.maps))
	for i, m := range d.maps {
		start := amapFirst + int64(i)*amapCovers
		b := make([]byte, pageSize)
		copy(b, m)
		run, best := 0, 0
		for bit := 0; bit < 496*8; bit++ {
			if m[bit/8]&(0x80>>(bit%8)) == 0 {
				free += 64
				run++
				best = max(best, run)
			} else {
				run = 0
			}
		}
		fmax[i] = byte(min(best, 255))
		pageTrailer(b, ptypeAMap, 0, uint64(start))
		if _, err := d.out.WriteAt(b, start); err != nil {
			return err
		}
	}
	for i := range d.maps {
		start := amapFirst + int64(i)*amapCovers
		for _, p := range mapPages(int64(i))[1:] {
			b := make([]byte, pageSize)
			var ptype byte
			switch p / pageSize {
			case 1: // deprecated: all the pages are «not free»
				ptype = ptypePMap
				for j := 0; j < 496; j++ {
					b[j] = 0xFF
				}
			case 2: // the longest free run of each AMap it maps
				ptype = ptypeFMap
				first := fmapFirstAt + (int64(i)-fmapFirstAt)/fmapEvery*fmapEvery
				for j := int64(0); j < fmapEvery && first+j < int64(len(fmax)); j++ {
					b[j] = fmax[first+j]
				}
			case 3: // deprecated: no PMap has free pages
				ptype = ptypeFPMap
				for j := 0; j < 496; j++ {
					b[j] = 0xFF
				}
			}
			pageTrailer(b, ptype, 0, uint64(start+p))
			if _, err := d.out.WriteAt(b, start+p); err != nil {
				return err
			}
		}
	}
	// an empty density list, as Outlook leaves it
	dl := make([]byte, pageSize)
	dlBID := d.nextPID
	d.nextPID++
	pageTrailer(dl, ptypeDL, sig(dlistAt, dlBID), dlBID)
	if _, err := d.out.WriteAt(dl, dlistAt); err != nil {
		return err
	}
	return d.header(eof, amapFirst+(sections-1)*amapCovers, free, nbt, bbt)
}

func (d *ndb) header(eof, amapLast, free int64, nbt, bbt btRef) error {
	h := make([]byte, headerSize)
	copy(h, "!BDN")
	h[8], h[9] = 0x53, 0x4D
	le.PutUint16(h[10:], 23) // wVer: Unicode
	le.PutUint16(h[12:], 19) // wVerClient
	h[14], h[15] = 1, 1
	le.PutUint64(h[32:], d.nextPID)
	le.PutUint32(h[40:], 1) // dwUnique
	for i, v := range d.rgnid {
		le.PutUint32(h[44+4*i:], v)
	}
	r := h[180:]
	le.PutUint64(r[4:], uint64(eof))
	le.PutUint64(r[12:], uint64(amapLast))
	le.PutUint64(r[20:], uint64(free))
	le.PutUint64(r[36:], nbt.bid)
	le.PutUint64(r[44:], uint64(nbt.ib))
	le.PutUint64(r[52:], bbt.bid)
	le.PutUint64(r[60:], uint64(bbt.ib))
	r[68] = 2 // fAMapValid: VALID_AMAP2
	for i := 256; i < 512; i++ {
		h[i] = 0xFF // the deprecated rgbFM and rgbFP
	}
	h[512] = 0x80 // bSentinel
	h[513] = 0    // bCryptMethod: none
	le.PutUint64(h[516:], d.nextBID)
	le.PutUint32(h[4:], crc(h[8:8+471]))
	le.PutUint32(h[524:], crc(h[8:8+516]))
	_, err := d.out.WriteAt(h, 0)
	return err
}
