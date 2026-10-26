package pst

import (
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

// The columns of the template tables, as Outlook makes them.
var (
	hierarchyColumns = []uint32{0x0E300102, TagReplChangenum, TagReplVersionHistory, TagReplFlags, TagDisplayName, TagContentCount,
		TagContentUnreadCount, TagSubfolders, TagContainerClass, TagPstHiddenCount, TagPstHiddenUnread, TagLtpRowID, TagLtpRowVer}
	contentsColumns = []uint32{TagImportance, TagMessageClass, TagSensitivity, TagSubject, TagClientSubmitTime,
		TagSentRepresentingName, TagMessageToMe, TagMessageCcMe, TagConversationTopic, TagConversationIndex, TagDisplayCc,
		TagDisplayTo, TagMessageDeliveryTime, TagMessageFlags, TagMessageSize, TagMessageStatus, 0x0E300102, TagReplChangenum,
		TagReplVersionHistory, TagReplFlags, TagReplCopiedFromVersion, TagReplCopiedFromItemID, TagItemTemporaryFlags,
		TagLastModificationTime, 0x30130102, TagSecureSubmitFlags, TagLtpRowID, TagLtpRowVer}
	assocColumns = []uint32{TagMessageClass, TagMessageFlags, TagMessageStatus, TagDisplayName, TagLtpRowID, TagLtpRowVer,
		0x6800001F, 0x6803000B, 0x68051003, 0x682F001F, 0x70030003, 0x70040102, 0x70050102, 0x7006001F, 0x70070003}
	searchColumns = []uint32{TagImportance, TagMessageClass, TagSensitivity, TagSubject, TagSentRepresentingName,
		TagMessageToMe, TagMessageCcMe, TagDisplayCc, TagDisplayTo, TagParentDisplay, TagMessageDeliveryTime, TagMessageFlags,
		TagMessageSize, TagMessageStatus, TagExchangeRemoteHeader, TagLastModificationTime, TagLtpParentNid, TagLtpRowID,
		TagLtpRowVer}
	attachmentColumns = []uint32{TagAttachSize, TagAttachFilename, TagAttachMethod, TagRenderingPosition, TagLtpRowID,
		TagLtpRowVer}
	recipientColumns = []uint32{TagRecipientType, TagResponsibility, TagRecordKey, TagObjectType, TagEntryID, TagDisplayName,
		TagAddressType, TagEmailAddress, TagSearchKey, TagDisplayType, TagSevenBitDisplayName, TagSendRichInfo, TagLtpRowID,
		TagLtpRowVer}
	// a letter's tables have more columns than the templates
	msgAttachmentColumns = append(append([]uint32{}, attachmentColumns...), TagDisplayName, TagAttachExtension,
		TagAttachLongFilename, TagAttachMimeTag, TagAttachContentID, TagAttachFlags, TagAttachmentHidden)
	msgRecipientColumns = append(append([]uint32{}, recipientColumns...), TagRowID, TagRecipientDisplayName, TagSMTPAddress)
)

// More tags of the letters' tables.
const (
	TagRowID                = 0x30000003
	TagRecipientDisplayName = 0x5FF6001F
	TagSMTPAddress          = 0x39FE001F
	TagSenderSMTPAddress    = 0x5D01001F
	TagSentRepresentingSMTP = 0x5D02001F
	tagReceiveFolderNID     = 0x66050003
)

// cellMax: strings longer than that are cut in the folders' tables (as MAPI tables do); the letter keeps them whole.
const cellMax = 255

// Folder is a folder of the file being written.
type Folder uint32

// Special folders: Outlook knows them by the store's entry IDs and the receive folder table.
type Special int

const (
	Normal Special = iota
	Inbox
	Sent
	Drafts
	Junk
	Outbox
)

type folder struct {
	nid           uint32
	parent        *folder
	name, class   string
	kids          []*folder
	contents      *tcBuilder
	count, unread int
	special       Special
}

// Writer writes a .pst: folders, then letters into them, then Close.
type Writer struct {
	d       *ndb
	uid     [16]byte
	name    string
	folders map[Folder]*folder
	root    *folder
	top     *folder // «Top of Personal Folders» (IPM subtree): the user's folders are under it
	search  *folder // «Search Root»
	deleted *folder // «Deleted Items»
	ver     uint32  // the version of the rows: one counter for the whole file
	closed  bool
}

// Create starts a .pst whose personal folders are called name.
func Create(out Output, name string) (*Writer, error) {
	w := &Writer{d: newNDB(out), name: name, folders: map[Folder]*folder{}}
	if _, err := rand.Read(w.uid[:]); err != nil {
		return nil, err
	}
	w.root = &folder{nid: nidRootFolder, contents: w.d.newTC(contentsColumns)}
	w.root.parent = w.root
	w.top = w.newFolder(w.root, "Top of Personal Folders", "")
	w.search = w.newFolder(w.root, "Search Root", "")
	w.deleted = w.newFolder(w.top, "Deleted Items", "IPF.Note")
	return w, nil
}

func (w *Writer) newFolder(parent *folder, name, class string) *folder {
	f := &folder{nid: w.d.newNID(nidTypeNormalFolder), parent: parent, name: name, class: class}
	f.contents = w.d.newTC(contentsColumns)
	parent.kids = append(parent.kids, f)
	w.folders[Folder(f.nid)] = f
	return f
}

// Top is the folder the user's folders go under.
func (w *Writer) Top() Folder { return Folder(w.top.nid) }

// DeletedItems is the folder «Deleted Items» every file has; Rename gives it the name of the mailbox's trash.
func (w *Writer) DeletedItems() Folder { return Folder(w.deleted.nid) }

// AddFolder makes a folder of letters under parent.
func (w *Writer) AddFolder(parent Folder, name string, special Special) (Folder, error) {
	p, ok := w.folders[parent]
	if !ok || p == w.search {
		return 0, fmt.Errorf("pst: no folder %#x", parent)
	}
	f := w.newFolder(p, name, "IPF.Note")
	f.special = special
	return Folder(f.nid), nil
}

// Rename gives a folder another name.
func (w *Writer) Rename(id Folder, name string) {
	if f, ok := w.folders[id]; ok {
		f.name = name
	}
}

// Recipient is a recipient of a letter.
type Recipient struct {
	Type        int // RecipientTo, RecipientCc, RecipientBcc
	Name, Email string
}

// Attachment is a file attached to a letter.
type Attachment struct {
	Filename, MimeType, ContentID string
	Inline                        bool // a picture of the letter's HTML, not shown as an attachment
	Data                          []byte
}

// Message is a letter: its properties (Prop), recipients and attachments.
type Message struct {
	Props       []Prop
	Recipients  []Recipient
	Attachments []Attachment
}

// Prop is a property of a letter; Value is a string, an int (or int32), an int64, a bool, a time.Time or []byte, as
// the tag's type is.
type Prop struct {
	Tag   uint32
	Value any
}

// encode encodes a value of the tag's type.
func encode(tag uint32, v any) ([]byte, error) {
	typ := uint16(tag)
	switch x := v.(type) {
	case string:
		if typ == ptString {
			return utf16le(x), nil
		}
		if typ == ptBinary || typ == ptString8 {
			return []byte(x), nil
		}
	case []byte:
		if typ == ptBinary {
			return x, nil
		}
	case bool:
		if typ == ptBoolean {
			if x {
				return []byte{1}, nil
			}
			return []byte{0}, nil
		}
	case int:
		return encode(tag, int64(x))
	case int32:
		return encode(tag, int64(x))
	case uint32:
		return encode(tag, int64(x))
	case int64:
		switch typ {
		case ptInt32:
			return le.AppendUint32(nil, uint32(x)), nil
		case ptInt16:
			return le.AppendUint16(nil, uint16(x)), nil
		case ptInt64:
			return le.AppendUint64(nil, uint64(x)), nil
		}
	case time.Time:
		if typ == ptTime {
			return le.AppendUint64(nil, uint64(filetime(x))), nil
		}
	}
	return nil, fmt.Errorf("pst: value %T for property %#08x", v, tag)
}

// filetime is the time as Windows keeps it: 100-nanosecond intervals since 1601.
func filetime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()*10_000_000 + int64(t.UTC().Nanosecond()/100) + 116444736000000000
}

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		le.PutUint16(b[2*i:], c)
	}
	return b
}

// entryID is the entry ID of an object of the file: the store's UID and the node.
func (w *Writer) entryID(nid uint32) []byte {
	b := make([]byte, 24)
	copy(b[4:], w.uid[:])
	le.PutUint32(b[20:], nid)
	return b
}

// oneOffEntryID is the entry ID of an address not in an address book ([MS-OXCDATA] 2.2.5.1), Unicode, no rich text.
func oneOffEntryID(name, email string) []byte {
	b := []byte{0, 0, 0, 0, 0x81, 0x2B, 0x1F, 0xA4, 0xBE, 0xA3, 0x10, 0x19, 0x9D, 0x6E, 0x00, 0xDD, 0x01, 0x0F, 0x54, 0x02,
		0, 0, 0x01, 0x90}
	for _, s := range []string{name, "SMTP", email} {
		b = append(b, utf16le(s)...)
		b = append(b, 0, 0)
	}
	return b
}

// searchKey is the search key of an address: «SMTP:» and the address in capitals, ending in a zero byte.
func searchKey(email string) []byte {
	return append([]byte("SMTP:"+strings.ToUpper(email)), 0)
}

// AddMessage writes a letter into a folder.
func (w *Writer) AddMessage(id Folder, m *Message) error {
	f, ok := w.folders[id]
	if !ok || f == w.search {
		return fmt.Errorf("pst: no folder %#x", id)
	}
	nid := w.d.newNID(nidTypeNormalMessage)
	props := map[uint32][]byte{}
	for _, p := range m.Props {
		if s, ok := p.Value.(string); ok && p.Tag == TagSubject {
			p.Value = encodeSubject(s)
		}
		v, err := encode(p.Tag, p.Value)
		if err != nil {
			return err
		}
		props[p.Tag] = v
	}
	now := time.Now()
	def := func(tag uint32, v any) {
		if _, ok := props[tag]; !ok {
			props[tag], _ = encode(tag, v)
		}
	}
	def(TagMessageClass, "IPM.Note")
	def(TagMessageStatus, 0)
	def(TagCreationTime, now)
	def(TagLastModificationTime, now)
	def(TagMessageSize, 0)
	def(TagImportance, 1)
	def(TagSensitivity, 0)
	// the sender and the one sent for: their address type, entry ID, search key, SMTP address, as Outlook keeps them
	for _, a := range []struct{ name, email, addrType, entryID, searchKey, smtp uint32 }{
		{TagSenderName, TagSenderEmail, TagSenderAddrType, TagSenderEntryID, TagSenderSearchKey, TagSenderSMTPAddress},
		{TagSentRepresentingName, TagSentRepresentingEmail, TagSentRepresentingAddrType, TagSentRepresentingEntryID,
			TagSentRepresentingSearch, TagSentRepresentingSMTP},
	} {
		v, ok := props[a.email]
		if !ok {
			continue
		}
		email, name := utf16String(v), utf16String(props[a.name])
		def(a.addrType, "SMTP")
		def(a.entryID, oneOffEntryID(name, email))
		def(a.searchKey, searchKey(email))
		def(a.smtp, email)
	}
	if v, ok := props[TagSubject]; ok {
		def(TagConversationTopic, normalizedSubject(utf16String(v)))
	}
	if _, ok := props[TagSearchKey]; !ok {
		k := md5.Sum(append(w.uid[:], le.AppendUint32(nil, nid)...))
		props[TagSearchKey] = k[:]
	}
	flags := uint32(0)
	if v, ok := props[TagMessageFlags]; ok {
		flags = le.Uint32(v)
	}
	if len(m.Attachments) > 0 {
		flags |= FlagHasAttach
	}
	props[TagMessageFlags] = le.AppendUint32(nil, flags)
	props[TagHasAttachments] = []byte{boolByte(len(m.Attachments) > 0)}
	var to, cc, bcc []string
	for _, r := range m.Recipients {
		name := r.Name
		if name == "" {
			name = r.Email
		}
		switch r.Type {
		case RecipientCc:
			cc = append(cc, name)
		case RecipientBcc:
			bcc = append(bcc, name)
		default:
			to = append(to, name)
		}
	}
	props[TagDisplayTo] = utf16le(strings.Join(to, "; "))
	props[TagDisplayCc] = utf16le(strings.Join(cc, "; "))
	props[TagDisplayBcc] = utf16le(strings.Join(bcc, "; "))

	var subs []subnode
	data, sub, err := w.d.tc(msgRecipientColumns, w.recipientRows(m.Recipients))
	if err != nil {
		return err
	}
	subs = append(subs, subnode{nid: nidRecipientTableTemplate, data: data, sub: sub})
	if len(m.Attachments) > 0 {
		var rows []row
		for i, a := range m.Attachments {
			anid := makeNID(nidTypeAttachment, uint32(0x400+i+1))
			ap := attachmentProps(a, i)
			data, sub, err := w.d.pc(ap)
			if err != nil {
				return err
			}
			subs = append(subs, subnode{nid: anid, data: data, sub: sub})
			r := row{id: anid, cells: map[uint32][]byte{}}
			for _, p := range ap {
				if p.tag != TagAttachDataBinary {
					r.cells[p.tag] = cut(p.tag, p.val)
				}
			}
			rows = append(rows, r)
		}
		data, sub, err := w.d.tc(msgAttachmentColumns, rows)
		if err != nil {
			return err
		}
		subs = append(subs, subnode{nid: nidAttachmentTableTemplate, data: data, sub: sub})
	}
	list := make([]prop, 0, len(props))
	for tag, v := range props {
		list = append(list, prop{tag, v})
	}
	data, sub, err = w.d.pc(list, subs...)
	if err != nil {
		return err
	}
	w.d.node(nid, data, sub, f.nid)

	w.ver++
	r := row{id: nid, cells: map[uint32][]byte{TagLtpRowVer: le.AppendUint32(nil, w.ver)}}
	for _, tag := range contentsColumns {
		if v, ok := props[tag]; ok {
			r.cells[tag] = cut(tag, v)
		}
	}
	if err := f.contents.add(r); err != nil {
		return err
	}
	f.count++
	if flags&FlagRead == 0 {
		f.unread++
	}
	return nil
}

// encodeSubject keeps a subject as Outlook does ([MS-PST] 2.5.3.1.1): the character 1, the length of the prefix
// («RE: », «Fwd: ») plus one, the subject. Readers that see no 1 parse the prefix themselves, and some of them take
// a first character below 0x20 — of a capital Cyrillic letter, 0x0410 to 0x042F, in its lower byte — for that mark.
func encodeSubject(s string) string {
	r := []rune(s)
	prefix := 0
	for i := 1; i <= 3 && i < len(r); i++ {
		if r[i-1] == ' ' || unicode.IsDigit(r[i-1]) {
			break
		}
		if r[i] == ':' {
			prefix = i + 1
			for prefix < len(r) && r[prefix] == ' ' {
				prefix++
			}
			break
		}
	}
	return string([]rune{1, rune(len(utf16.Encode(r[:prefix])) + 1)}) + s
}

// normalizedSubject is the subject encoded without the mark and the prefix.
func normalizedSubject(s string) string {
	u := utf16.Encode([]rune(s))
	if len(u) >= 2 && u[0] == 1 {
		if n := 2 + int(u[1]) - 1; n <= len(u) {
			return string(utf16.Decode(u[n:]))
		}
	}
	return s
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// cut cuts a string of a table's cell to cellMax characters.
func cut(tag uint32, v []byte) []byte {
	if uint16(tag) == ptString && len(v) > 2*cellMax {
		n := 2 * cellMax
		if c := le.Uint16(v[n-2:]); c >= 0xD800 && c < 0xDC00 { // not between the halves of a surrogate pair
			n -= 2
		}
		return v[:n]
	}
	return v
}

func (w *Writer) recipientRows(rs []Recipient) []row {
	rows := make([]row, 0, len(rs))
	for i, r := range rs {
		name := r.Name
		if name == "" {
			name = r.Email
		}
		typ := r.Type
		if typ == 0 {
			typ = RecipientTo
		}
		cells := map[uint32][]byte{
			TagRecipientType:        le.AppendUint32(nil, uint32(typ)),
			TagResponsibility:       {1},
			TagObjectType:           le.AppendUint32(nil, 6), // MAPI_MAILUSER
			TagEntryID:              oneOffEntryID(name, r.Email),
			TagRecordKey:            oneOffEntryID(name, r.Email),
			TagDisplayName:          cut(TagDisplayName, utf16le(name)),
			TagAddressType:          utf16le("SMTP"),
			TagEmailAddress:         utf16le(r.Email),
			TagSearchKey:            searchKey(r.Email),
			TagDisplayType:          le.AppendUint32(nil, 0),
			TagSevenBitDisplayName:  cut(TagDisplayName, utf16le(name)),
			TagSendRichInfo:         {0},
			TagRowID:                le.AppendUint32(nil, uint32(i)),
			TagRecipientDisplayName: cut(TagDisplayName, utf16le(name)),
			TagSMTPAddress:          utf16le(r.Email),
		}
		rows = append(rows, row{id: uint32(i), cells: cells})
	}
	return rows
}

// attachmentProps are the properties of an attachment by value.
func attachmentProps(a Attachment, i int) []prop {
	name := a.Filename
	if name == "" {
		name = fmt.Sprintf("attachment%d", i+1)
	}
	ext := ""
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		ext = name[dot:]
	}
	ps := []prop{
		{TagAttachSize, le.AppendUint32(nil, uint32(len(a.Data)))},
		{TagAttachMethod, le.AppendUint32(nil, 1)}, // ATTACH_BY_VALUE
		{TagRenderingPosition, le.AppendUint32(nil, 0xFFFFFFFF)},
		{TagObjectType, le.AppendUint32(nil, 7)}, // MAPI_ATTACH
		{TagDisplayName, utf16le(name)},
		{TagAttachFilename, utf16le(name)},
		{TagAttachLongFilename, utf16le(name)},
		{TagAttachDataBinary, a.Data},
		{TagAttachFlags, le.AppendUint32(nil, 0)},
	}
	if ext != "" {
		ps = append(ps, prop{TagAttachExtension, utf16le(ext)})
	}
	if a.MimeType != "" {
		ps = append(ps, prop{TagAttachMimeTag, utf16le(a.MimeType)})
	}
	if a.ContentID != "" {
		ps = append(ps, prop{TagAttachContentID, utf16le(a.ContentID)})
	}
	if a.Inline {
		ps = append(ps, prop{TagAttachmentHidden, []byte{1}})
	}
	return ps
}

// Close writes the folders, the store and what the file needs besides, then the B-trees and the header.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	d := w.d
	if err := w.writeFolder(w.root); err != nil {
		return err
	}
	// «SPAM Search Folder 2»: a search folder Outlook expects under the root
	const spam = 0x2223
	data, sub, err := d.pc(folderProps("SPAM Search Folder 2", "", 0, 0, false))
	if err != nil {
		return err
	}
	d.node(spam, data, sub, nidRootFolder)
	if data, sub, err = d.tc(searchColumns, nil); err != nil {
		return err
	}
	d.node(makeNID(nidTypeSearchContents, spam>>5), data, sub, 0)

	inbox, mask := uint32(nidRootFolder), uint32(0x89) // the subtree, the wastebasket and the finder are valid
	store := []prop{
		{TagRecordKey, w.uid[:]},
		{TagDisplayName, utf16le(w.name)},
		{TagIpmSubTreeEntryID, w.entryID(w.top.nid)},
		{TagIpmWastebasketEntryID, w.entryID(w.deleted.nid)},
		{TagFinderEntryID, w.entryID(w.search.nid)},
	}
	for _, f := range w.folders {
		switch f.special {
		case Inbox:
			inbox, mask = f.nid, mask|0x02
		case Sent:
			store, mask = append(store, prop{0x35E40102, w.entryID(f.nid)}), mask|0x10
		case Outbox:
			store, mask = append(store, prop{0x35E20102, w.entryID(f.nid)}), mask|0x04
		}
	}
	store = append(store, prop{TagValidFolderMask, le.AppendUint32(nil, mask)})
	if data, sub, err = d.pc(store); err != nil {
		return err
	}
	d.node(nidMessageStore, data, sub, 0)

	// the receive folder table: where letters of each class arrive
	var rows []row
	for i, class := range []string{"", "IPM", "Report.IPM", "IPC"} {
		to := inbox
		if class == "IPC" {
			to = nidRootFolder
		}
		rows = append(rows, row{id: uint32(i + 1), cells: map[uint32][]byte{TagMessageClass: utf16le(class),
			tagReceiveFolderNID: le.AppendUint32(nil, to)}})
	}
	if data, sub, err = d.tc([]uint32{TagMessageClass, tagReceiveFolderNID}, rows); err != nil {
		return err
	}
	d.node(0x62B, data, sub, 0)

	names := nameMap([]namedProp{{guid: 2, name: "Keywords"}, {guid: 3, id: 0x8514}})
	if data, sub, err = d.pc(names); err != nil {
		return err
	}
	d.node(nidNameToIDMap, data, sub, 0)
	d.node(nidSearchManagementQueue, 0, 0, 0)
	d.node(nidSearchActivityList, 0, 0, 0)
	for _, t := range []struct {
		nid  uint32
		cols []uint32
	}{
		{nidHierarchyTableTemplate, hierarchyColumns},
		{nidContentsTableTemplate, contentsColumns},
		{nidAssocContentsTemplate, assocColumns},
		{nidSearchContentsTemplate, searchColumns},
		{nidAttachmentTableTemplate, attachmentColumns},
		{nidRecipientTableTemplate, recipientColumns},
	} {
		if data, sub, err = d.tc(t.cols, nil); err != nil {
			return err
		}
		d.node(t.nid, data, sub, 0)
	}
	return d.close()
}

func folderProps(name, class string, count, unread int, kids bool) []prop {
	ps := []prop{
		{TagDisplayName, utf16le(name)},
		{TagContentCount, le.AppendUint32(nil, uint32(count))},
		{TagContentUnreadCount, le.AppendUint32(nil, uint32(unread))},
		{TagSubfolders, []byte{boolByte(kids)}},
	}
	if class != "" {
		ps = append(ps, prop{TagContainerClass, utf16le(class)})
	}
	return ps
}

// writeFolder writes a folder and the folders under it: its PC, the tables of its subfolders, letters and associated
// contents.
func (w *Writer) writeFolder(f *folder) error {
	d := w.d
	kids := len(f.kids) > 0
	if f == w.root {
		kids = true // the root has the search folder too
	}
	data, sub, err := d.pc(folderProps(f.name, f.class, f.count, f.unread, kids))
	if err != nil {
		return err
	}
	d.node(f.nid, data, sub, f.parent.nid)
	var rows []row
	for _, k := range f.kids {
		w.ver++
		r := row{id: k.nid, cells: map[uint32][]byte{TagLtpRowVer: le.AppendUint32(nil, w.ver)}}
		for _, p := range folderProps(k.name, k.class, k.count, k.unread, len(k.kids) > 0) {
			r.cells[p.tag] = cut(p.tag, p.val)
		}
		rows = append(rows, r)
	}
	if f == w.root {
		w.ver++
		r := row{id: 0x2223, cells: map[uint32][]byte{TagLtpRowVer: le.AppendUint32(nil, w.ver)}}
		for _, p := range folderProps("SPAM Search Folder 2", "", 0, 0, false) {
			r.cells[p.tag] = p.val
		}
		rows = append(rows, r)
	}
	index := f.nid >> 5
	if data, sub, err = d.tc(hierarchyColumns, rows); err != nil {
		return err
	}
	d.node(makeNID(nidTypeHierarchyTable, index), data, sub, 0)
	if data, sub, err = f.contents.write(); err != nil {
		return err
	}
	d.node(makeNID(nidTypeContentsTable, index), data, sub, 0)
	if data, sub, err = d.tc(assocColumns, nil); err != nil {
		return err
	}
	d.node(makeNID(nidTypeAssocContents, index), data, sub, 0)
	for _, k := range f.kids {
		if err := w.writeFolder(k); err != nil {
			return err
		}
	}
	return nil
}

// namedProp is a named property: a GUID (1 — PS_MAPI, 2 — PS_PUBLIC_STRINGS, 3 and on — of the GUID stream, in
// nameGUIDs) with a name or a number.
type namedProp struct {
	guid uint16
	name string
	id   uint32
}

// nameGUIDs is the GUID stream: PSETID_Common {00062008-0000-0000-C000-000000000046}.
var nameGUIDs = []byte{0x08, 0x20, 0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}

// nameMap is the name-to-ID map of the named properties (their IDs 0x8000 on, in order): its streams and its hash
// buckets ([MS-PST] 2.4.7).
func nameMap(names []namedProp) []prop {
	const buckets = 251
	var entries, strs []byte
	hash := map[uint32][]byte{}
	for i, n := range names {
		id, kind := n.id, n.guid<<1
		hid := id
		if n.name != "" {
			s := utf16le(n.name)
			id, kind, hid = uint32(len(strs)), kind|1, crc(s)
			strs = le.AppendUint32(strs, uint32(len(s)))
			strs = append(strs, s...)
			for len(strs)%4 != 0 {
				strs = append(strs, 0)
			}
		}
		rec := le.AppendUint16(le.AppendUint16(le.AppendUint32(nil, id), kind), uint16(i))
		entries = append(entries, rec...)
		b := (hid ^ uint32(kind)) % buckets
		hash[b] = le.AppendUint16(le.AppendUint16(le.AppendUint32(hash[b], hid), kind), uint16(i))
	}
	ps := []prop{
		{TagNameidBucketCount, le.AppendUint32(nil, buckets)},
		{TagNameidStreamGUID, nameGUIDs},
		{TagNameidStreamEntry, entries},
		{TagNameidStreamString, strs},
	}
	for b, recs := range hash {
		ps = append(ps, prop{(0x1000+b)<<16 | ptBinary, recs})
	}
	return ps
}
