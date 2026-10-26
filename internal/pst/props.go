package pst

// Property types ([MS-OXCDATA] 2.11.1).
const (
	ptInt16    = 0x0002
	ptInt32    = 0x0003
	ptFloat32  = 0x0004
	ptFloat64  = 0x0005
	ptCurrency = 0x0006
	ptAppTime  = 0x0007
	ptError    = 0x000A
	ptBoolean  = 0x000B
	ptObject   = 0x000D
	ptInt64    = 0x0014
	ptString8  = 0x001E
	ptString   = 0x001F
	ptTime     = 0x0040
	ptGUID     = 0x0048
	ptBinary   = 0x0102
	ptMVInt32  = 0x1003
	ptMVString = 0x101F
)

// Property tags: the ID in the upper 16 bits, the type in the lower.
const (
	TagImportance               = 0x00170003
	TagMessageClass             = 0x001A001F
	TagSensitivity              = 0x00360003
	TagSubject                  = 0x0037001F
	TagClientSubmitTime         = 0x00390040
	TagSentRepresentingName     = 0x0042001F
	TagSentRepresentingAddrType = 0x0064001F
	TagSentRepresentingEmail    = 0x0065001F
	TagSentRepresentingEntryID  = 0x00410102
	TagSentRepresentingSearch   = 0x003B0102
	TagMessageToMe              = 0x0057000B
	TagMessageCcMe              = 0x0058000B
	TagConversationTopic        = 0x0070001F
	TagConversationIndex        = 0x00710102
	TagReceivedByName           = 0x0040001F
	TagReceivedByEmail          = 0x0076001F
	TagReceivedByAddrType       = 0x0075001F
	TagTransportHeaders         = 0x007D001F
	TagSenderEntryID            = 0x0C190102
	TagSenderName               = 0x0C1A001F
	TagSenderSearchKey          = 0x0C1D0102
	TagSenderAddrType           = 0x0C1E001F
	TagSenderEmail              = 0x0C1F001F
	TagRecipientType            = 0x0C150003
	TagDeleteAfterSubmit        = 0x0E010003
	TagDisplayBcc               = 0x0E02001F
	TagDisplayCc                = 0x0E03001F
	TagDisplayTo                = 0x0E04001F
	TagParentDisplay            = 0x0E05001F
	TagMessageDeliveryTime      = 0x0E060040
	TagMessageFlags             = 0x0E070003
	TagMessageSize              = 0x0E080003
	TagResponsibility           = 0x0E0F000B
	TagMessageStatus            = 0x0E170003
	TagAttachSize               = 0x0E200003
	TagHasAttachments           = 0x0E1B000B
	TagReplItemID               = 0x0E300003
	TagReplChangenum            = 0x0E330014
	TagReplVersionHistory       = 0x0E340102
	TagReplFlags                = 0x0E380003
	TagReplCopiedFromVersion    = 0x0E3C0102
	TagReplCopiedFromItemID     = 0x0E3D0102
	TagItemTemporaryFlags       = 0x10970003
	TagBody                     = 0x1000001F
	TagHTML                     = 0x10130102
	TagInternetMessageID        = 0x1035001F
	TagInReplyTo                = 0x1042001F
	TagInternetReferences       = 0x1039001F
	TagInternetCodepage         = 0x3FDE0003
	TagMessageCodepage          = 0x3FFD0003
	TagNativeBody               = 0x10160003
	TagRecordKey                = 0x0FF90102
	TagObjectType               = 0x0FFE0003
	TagEntryID                  = 0x0FFF0102
	TagDisplayName              = 0x3001001F
	TagAddressType              = 0x3002001F
	TagEmailAddress             = 0x3003001F
	TagCreationTime             = 0x30070040
	TagLastModificationTime     = 0x30080040
	TagSearchKey                = 0x300B0102
	TagValidFolderMask          = 0x35DF0003
	TagIpmSubTreeEntryID        = 0x35E00102
	TagIpmWastebasketEntryID    = 0x35E30102
	TagFinderEntryID            = 0x35E70102
	TagContentCount             = 0x36020003
	TagContentUnreadCount       = 0x36030003
	TagSubfolders               = 0x360A000B
	TagContainerClass           = 0x3613001F
	TagAttachDataBinary         = 0x37010102
	TagAttachExtension          = 0x3703001F
	TagAttachFilename           = 0x3704001F
	TagAttachMethod             = 0x37050003
	TagAttachLongFilename       = 0x3707001F
	TagRenderingPosition        = 0x370B0003
	TagAttachMimeTag            = 0x370E001F
	TagAttachContentID          = 0x3712001F
	TagAttachFlags              = 0x37140003
	TagDisplayType              = 0x39000003
	TagSevenBitDisplayName      = 0x39FF001F
	TagSendRichInfo             = 0x3A40000B
	TagSecureSubmitFlags        = 0x65C60003
	TagPstHiddenCount           = 0x66350003
	TagPstHiddenUnread          = 0x66360003
	TagLtpParentNid             = 0x67F10003
	TagLtpRowID                 = 0x67F20003
	TagLtpRowVer                = 0x67F30003
	TagExchangeRemoteHeader     = 0x0E2A000B
	TagAttachmentHidden         = 0x7FFE000B
	TagFlagStatus               = 0x10900003
	TagLastVerbExecuted         = 0x10810003
	TagNameidBucketCount        = 0x00010003
	TagNameidStreamGUID         = 0x00020102
	TagNameidStreamEntry        = 0x00030102
	TagNameidStreamString       = 0x00040102
)

// Message flags (PidTagMessageFlags).
const (
	FlagRead       = 0x01
	FlagUnmodified = 0x02
	FlagUnsent     = 0x08
	FlagHasAttach  = 0x10
	FlagFromMe     = 0x20
)

// Recipient types.
const (
	RecipientTo  = 1
	RecipientCc  = 2
	RecipientBcc = 3
)
