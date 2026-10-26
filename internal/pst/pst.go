// Package pst writes and reads Outlook's personal folders files (.pst) of the Unicode format, as [MS-PST] tells.
//
// The file has three layers. The NDB (node database) keeps nodes: each is a stream of data in blocks and a tree of
// subnodes, found through two B-trees — of nodes (NBT) and of blocks (BBT); allocation maps (AMap) tell which space
// is taken. The LTP (lists, tables, properties) makes a heap in a node's data (HN), a B-tree in that heap (BTH) and on
// them property contexts (PC, an object's properties) and table contexts (TC, rows of columns). The messaging layer
// makes of them the store, folders (a PC and three tables: subfolders, contents, associated contents), letters (a PC
// with the tables of recipients and attachments in its subnodes) and attachments.
package pst

import (
	"encoding/binary"
	"hash/crc32"
)

var le = binary.LittleEndian

// Sizes of the Unicode format.
const (
	pageSize      = 512
	blockMax      = 8192                // the largest block, with its trailer
	blockTrailer  = 16                  // cb, wSig, dwCRC, bid
	blockData     = blockMax - 16       // the most data a block keeps: 8176
	xblockEntries = (blockMax - 24) / 8 // the BIDs an XBLOCK or XXBLOCK keeps: 1021
	heapItemMax   = 3580                // the largest item of a heap; larger values go into subnodes

	amapFirst    = 0x4400       // the first allocation map; the data section starts with it
	amapCovers   = 496 * 8 * 64 // the bytes an AMap maps: 253 952
	pmapEvery    = 8            // a page map follows every 8th AMap
	fmapFirstAt  = 128          // the first free map follows the 128th AMap (the header maps the first 128)
	fmapEvery    = 496          // then every 496 AMaps
	fpmapFirstAt = 128 * 64     // the first free page map follows the 8192nd AMap
	fpmapEvery   = 496 * 64     // then every 31 744 AMaps
	dlistAt      = 0x4200       // the density list
	headerSize   = 564          // the header's bytes (the rest of the first 0x4200 bytes is reserved)
	ptypeBBT     = 0x80         // page types
	ptypeNBT     = 0x81
	ptypeFMap    = 0x82
	ptypePMap    = 0x83
	ptypeAMap    = 0x84
	ptypeFPMap   = 0x85
	ptypeDL      = 0x86
)

// NID types: the low 5 bits of a node's ID.
const (
	nidTypeHID             = 0x00
	nidTypeInternal        = 0x01
	nidTypeNormalFolder    = 0x02
	nidTypeSearchFolder    = 0x03
	nidTypeNormalMessage   = 0x04
	nidTypeAttachment      = 0x05
	nidTypeAssocMessage    = 0x08
	nidTypeHierarchyTable  = 0x0D
	nidTypeContentsTable   = 0x0E
	nidTypeAssocContents   = 0x0F
	nidTypeSearchContents  = 0x10
	nidTypeAttachmentTable = 0x11
	nidTypeRecipientTable  = 0x12
	nidTypeLTP             = 0x1F
)

// The special nodes of the messaging layer.
const (
	nidMessageStore            = 0x21
	nidNameToIDMap             = 0x61
	nidNormalFolderTemplate    = 0xA1
	nidSearchFolderTemplate    = 0xC1
	nidRootFolder              = 0x122
	nidSearchManagementQueue   = 0x1E1
	nidSearchActivityList      = 0x201
	nidHierarchyTableTemplate  = 0x60D
	nidContentsTableTemplate   = 0x60E
	nidAssocContentsTemplate   = 0x60F
	nidSearchContentsTemplate  = 0x610
	nidAttachmentTableTemplate = 0x671
	nidRecipientTableTemplate  = 0x692
)

func makeNID(typ, index uint32) uint32 { return index<<5 | typ }

func nidType(nid uint32) uint32 { return nid & 0x1F }

// The client signatures of a heap.
const (
	hnSig    = 0xEC
	bTypeTC  = 0x7C
	bTypeBTH = 0xB5
	bTypePC  = 0xBC
)

// crc is the CRC of [MS-PST] 5.3: the reflected CRC-32 of IEEE with no seed and no final inversion.
func crc(b []byte) uint32 { return ^crc32.Update(0xFFFFFFFF, crc32.IEEETable, b) }

// sig is the signature of a block or page at the offset ib with the ID bid ([MS-PST] 5.5).
func sig(ib, bid uint64) uint16 {
	x := uint32(ib ^ bid)
	return uint16(x>>16) ^ uint16(x)
}

// unpermute decodes the data of a block of a file kept with NDB_CRYPT_PERMUTE ([MS-PST] 5.1).
func unpermute(b []byte) {
	for i, c := range b {
		b[i] = permuteDecode[c]
	}
}

var permuteDecode = [256]byte{
	71, 241, 180, 230, 11, 106, 114, 72, 133, 78, 158, 235, 226, 248, 148, 83, 224, 187, 160, 2,
	232, 90, 9, 171, 219, 227, 186, 198, 124, 195, 16, 221, 57, 5, 150, 48, 245, 55, 96, 130,
	140, 201, 19, 74, 107, 29, 243, 251, 143, 38, 151, 202, 145, 23, 1, 196, 50, 45, 110, 49,
	149, 255, 217, 35, 209, 0, 94, 121, 220, 68, 59, 26, 40, 197, 97, 87, 32, 144, 61, 131,
	185, 67, 190, 103, 210, 70, 66, 118, 192, 109, 91, 126, 178, 15, 22, 41, 60, 169, 3, 84,
	13, 218, 93, 223, 246, 183, 199, 98, 205, 141, 6, 211, 105, 92, 134, 214, 20, 247, 165, 102,
	117, 172, 177, 233, 69, 33, 112, 12, 135, 159, 116, 164, 34, 76, 111, 191, 31, 86, 170, 46,
	179, 120, 51, 80, 176, 163, 146, 188, 207, 25, 28, 167, 99, 203, 30, 77, 62, 75, 27, 155,
	79, 231, 240, 238, 173, 58, 181, 89, 4, 234, 64, 85, 37, 81, 229, 122, 137, 56, 104, 82,
	123, 252, 39, 174, 215, 189, 250, 7, 244, 204, 142, 95, 239, 53, 156, 132, 43, 21, 213, 119,
	52, 73, 182, 18, 10, 127, 113, 136, 253, 157, 24, 65, 125, 147, 216, 88, 44, 206, 254, 36,
	175, 222, 184, 54, 200, 161, 128, 166, 153, 152, 168, 47, 14, 129, 101, 115, 228, 194, 162, 138,
	212, 225, 17, 208, 8, 139, 42, 242, 237, 154, 100, 63, 193, 108, 249, 236,
}
