// SPDX-License-Identifier: AGPL-3.0-or-later

package wbxml

// Code pages of Exchange ActiveSync as defined in [MS-ASWBXML] section 2.1.2.1.
// Each page lists its tags in token order starting at 0x05; an empty string
// marks a token that is not assigned.

// Page numbers.
const (
	PageAirSync           byte = 0
	PageContacts          byte = 1
	PageEmail             byte = 2
	PageAirNotify         byte = 3
	PageCalendar          byte = 4
	PageMove              byte = 5
	PageItemEstimate      byte = 6
	PageFolderHierarchy   byte = 7
	PageMeetingResponse   byte = 8
	PageTasks             byte = 9
	PageResolveRecipients byte = 10
	PageValidateCert      byte = 11
	PageContacts2         byte = 12
	PagePing              byte = 13
	PageProvision         byte = 14
	PageSearch            byte = 15
	PageGAL               byte = 16
	PageAirSyncBase       byte = 17
	PageSettings          byte = 18
	PageDocumentLibrary   byte = 19
	PageItemOperations    byte = 20
	PageComposeMail       byte = 21
	PageEmail2            byte = 22
	PageNotes             byte = 23
	PageRightsManagement  byte = 24
)

// codePage is one WBXML code page.
type codePage struct {
	name string
	// tags[i] is the tag with token 0x05+i.
	tags []string
}

const firstToken = 0x05

var codePages = [...]codePage{
	PageAirSync: {"AirSync", []string{
		"Sync", "Responses", "Add", "Change", "Delete", "Fetch", "SyncKey", "ClientId", "ServerId", "Status",
		"Collection", "Class", "Version", "CollectionId", "GetChanges", "MoreAvailable", "WindowSize", "Commands",
		"Options", "FilterType", "Truncation", "RTFTruncation", "Conflict", "Collections", "ApplicationData",
		"DeletesAsMoves", "NotifyGUID", "Supported", "SoftDelete", "MIMESupport", "MIMETruncation", "Wait", "Limit",
		"Partial", "ConversationMode", "MaxItems", "HeartbeatInterval",
	}},
	PageContacts: {"Contacts", []string{
		"Anniversary", "AssistantName", "AssistantPhoneNumber", "Birthday", "Body", "BodySize", "BodyTruncated",
		"Business2PhoneNumber", "BusinessCity", "BusinessCountry", "BusinessPostalCode", "BusinessState",
		"BusinessStreet", "BusinessFaxNumber", "BusinessPhoneNumber", "CarPhoneNumber", "Categories", "Category",
		"Children", "Child", "CompanyName", "Department", "Email1Address", "Email2Address", "Email3Address",
		"FileAs", "FirstName", "Home2PhoneNumber", "HomeCity", "HomeCountry", "HomePostalCode", "HomeState",
		"HomeStreet", "HomeFaxNumber", "HomePhoneNumber", "JobTitle", "LastName", "MiddleName",
		"MobilePhoneNumber", "OfficeLocation", "OtherCity", "OtherCountry", "OtherPostalCode", "OtherState",
		"OtherStreet", "PagerNumber", "RadioPhoneNumber", "Spouse", "Suffix", "Title", "WebPage",
		"YomiCompanyName", "YomiFirstName", "YomiLastName", "CompressedRTF", "Picture", "Alias", "WeightedRank",
	}},
	PageEmail: {"Email", []string{
		"Attachment", "Attachments", "AttName", "AttSize", "Att0Id", "AttMethod", "AttRemoved", "Body", "BodySize",
		"BodyTruncated", "DateReceived", "DisplayName", "DisplayTo", "Importance", "MessageClass", "Subject",
		"Read", "To", "Cc", "From", "ReplyTo", "AllDayEvent", "Categories", "Category", "DtStamp", "EndTime",
		"InstanceType", "BusyStatus", "Location", "MeetingRequest", "Organizer", "RecurrenceId", "Reminder",
		"ResponseRequested", "Recurrences", "Recurrence", "Type", "Until", "Occurrences", "Interval",
		"DayOfWeek", "DayOfMonth", "WeekOfMonth", "MonthOfYear", "StartTime", "Sensitivity", "TimeZone",
		"GlobalObjId", "ThreadTopic", "MIMEData", "MIMETruncated", "MIMESize", "InternetCPID", "Flag", "Status",
		"ContentClass", "FlagType", "CompleteTime", "DisallowNewTimeProposal",
	}},
	PageAirNotify: {"AirNotify", []string{
		"Notify", "Notification", "Version", "LifeTime", "DeviceInfo", "Enable", "Folder", "ServerId",
		"DeviceAddress", "ValidCarrierProfiles", "CarrierProfile", "Status", "Responses", "Devices", "Device",
		"Id", "Expiry", "NotifyGUID", "DeviceFriendlyName",
	}},
	PageCalendar: {"Calendar", []string{
		"TimeZone", "AllDayEvent", "Attendees", "Attendee", "Email", "Name", "Body", "BodyTruncated", "BusyStatus",
		"Categories", "Category", "CompressedRTF", "DtStamp", "EndTime", "Exception", "Exceptions", "Deleted",
		"ExceptionStartTime", "Location", "MeetingStatus", "OrganizerEmail", "OrganizerName", "Recurrence",
		"Type", "Until", "Occurrences", "Interval", "DayOfWeek", "DayOfMonth", "WeekOfMonth", "MonthOfYear",
		"Reminder", "Sensitivity", "Subject", "StartTime", "UID", "AttendeeStatus", "AttendeeType",
		"", "", "", "", "", "", "", "", // 0x2B-0x32 are not used
		"DisallowNewTimeProposal", "ResponseRequested", "AppointmentReplyTime", "ResponseType",
		"CalendarType", "IsLeapMonth", "FirstDayOfWeek", "OnlineMeetingConfLink", "OnlineMeetingExternalLink",
		"ClientUid",
	}},
	PageMove: {"Move", []string{
		"MoveItems", "Move", "SrcMsgId", "SrcFldId", "DstFldId", "Response", "Status", "DstMsgId",
	}},
	PageItemEstimate: {"ItemEstimate", []string{
		"GetItemEstimate", "Version", "Collections", "Collection", "Class", "CollectionId", "DateTime",
		"Estimate", "Response", "Status",
	}},
	PageFolderHierarchy: {"FolderHierarchy", []string{
		"Folders", "Folder", "DisplayName", "ServerId", "ParentId", "Type", "Response", "Status",
		"ContentClass", "Changes", "Add", "Delete", "Update", "SyncKey", "FolderCreate", "FolderDelete",
		"FolderUpdate", "FolderSync", "Count", "Version",
	}},
	PageMeetingResponse: {"MeetingResponse", []string{
		"CalendarId", "CollectionId", "MeetingResponse", "RequestId", "Request", "Result", "Status",
		"UserResponse", "Version", "InstanceId", "", "ProposedStartTime", "ProposedEndTime", "SendResponse",
	}},
	PageTasks: {"Tasks", []string{
		"Body", "BodySize", "BodyTruncated", "Categories", "Category", "Complete", "DateCompleted", "DueDate",
		"UtcDueDate", "Importance", "Recurrence", "Type", "Start", "Until", "Occurrences", "Interval",
		"DayOfMonth", "DayOfWeek", "WeekOfMonth", "MonthOfYear", "Regenerate", "DeadOccur", "ReminderSet",
		"ReminderTime", "Sensitivity", "StartDate", "UtcStartDate", "Subject", "CompressedRTF", "OrdinalDate",
		"SubOrdinalDate", "CalendarType", "IsLeapMonth", "FirstDayOfWeek",
	}},
	PageResolveRecipients: {"ResolveRecipients", []string{
		"ResolveRecipients", "Response", "Status", "Type", "Recipient", "DisplayName", "EmailAddress",
		"Certificates", "Certificate", "MiniCertificate", "Options", "To", "CertificateRetrieval",
		"RecipientCount", "MaxCertificates", "MaxAmbiguousRecipients", "CertificateCount", "Availability",
		"StartTime", "EndTime", "MergedFreeBusy", "Picture", "MaxSize", "Data", "MaxPictures",
	}},
	PageValidateCert: {"ValidateCert", []string{
		"ValidateCert", "Certificates", "Certificate", "CertificateChain", "CheckCRL", "Status",
	}},
	PageContacts2: {"Contacts2", []string{
		"CustomerId", "GovernmentId", "IMAddress", "IMAddress2", "IMAddress3", "ManagerName",
		"CompanyMainPhone", "AccountName", "NickName", "MMS",
	}},
	PagePing: {"Ping", []string{
		"Ping", "AutdState", "Status", "HeartbeatInterval", "Folders", "Folder", "Id", "Class", "MaxFolders",
	}},
	PageProvision: {"Provision", []string{
		"Provision", "Policies", "Policy", "PolicyType", "PolicyKey", "Data", "Status", "RemoteWipe",
		"EASProvisionDoc", "DevicePasswordEnabled", "AlphanumericDevicePasswordRequired",
		"RequireStorageCardEncryption", "PasswordRecoveryEnabled", "DocumentBrowseEnabled", "AttachmentsEnabled",
		"MinDevicePasswordLength", "MaxInactivityTimeDeviceLock", "MaxDevicePasswordFailedAttempts",
		"MaxAttachmentSize", "AllowSimpleDevicePassword", "DevicePasswordExpiration", "DevicePasswordHistory",
		"AllowStorageCard", "AllowCamera", "RequireDeviceEncryption", "AllowUnsignedApplications",
		"AllowUnsignedInstallationPackages", "MinDevicePasswordComplexCharacters", "AllowWiFi",
		"AllowTextMessaging", "AllowPOPIMAPEmail", "AllowBluetooth", "AllowIrDA", "RequireManualSyncWhenRoaming",
		"AllowDesktopSync", "MaxCalendarAgeFilter", "AllowHTMLEmail", "MaxEmailAgeFilter",
		"MaxEmailBodyTruncationSize", "MaxEmailHTMLBodyTruncationSize", "RequireSignedSMIMEMessages",
		"RequireEncryptedSMIMEMessages", "RequireSignedSMIMEAlgorithm", "RequireEncryptionSMIMEAlgorithm",
		"AllowSMIMEEncryptionAlgorithmNegotiation", "AllowSMIMESoftCerts", "AllowBrowser", "AllowConsumerEmail",
		"AllowRemoteDesktop", "AllowInternetSharing", "UnapprovedInROMApplicationList", "ApplicationName",
		"ApprovedApplicationList", "Hash", "AccountOnlyRemoteWipe",
	}},
	PageSearch: {"Search", []string{
		"Search", "Stores", "Store", "Name", "Query", "Options", "Range", "Status", "Response", "Result",
		"Properties", "Total", "EqualTo", "Value", "And", "Or", "FreeText", "SubstringOp", "DeepTraversal",
		"LongId", "RebuildResults", "LessThan", "GreaterThan", "Schema", "Supported", "UserName", "Password",
		"ConversationId", "Picture", "MaxSize", "MaxPictures",
	}},
	PageGAL: {"GAL", []string{
		"DisplayName", "Phone", "Office", "Title", "Company", "Alias", "FirstName", "LastName", "HomePhone",
		"MobilePhone", "EmailAddress", "Picture", "Status", "Data",
	}},
	PageAirSyncBase: {"AirSyncBase", []string{
		"BodyPreference", "Type", "TruncationSize", "AllOrNone", "", "Body", "Data", "EstimatedDataSize",
		"Truncated", "Attachments", "Attachment", "DisplayName", "FileReference", "Method", "ContentId",
		"ContentLocation", "IsInline", "NativeBodyType", "ContentType", "Preview", "BodyPartPreference",
		"BodyPart", "Status", "Add", "Delete", "ClientId", "Content", "Location", "Annotation", "Street",
		"City", "State", "Country", "PostalCode", "Latitude", "Longitude", "Accuracy", "Altitude",
		"AltitudeAccuracy", "LocationUri", "InstanceId",
	}},
	PageSettings: {"Settings", []string{
		"Settings", "Status", "Get", "Set", "Oof", "OofState", "StartTime", "EndTime", "OofMessage",
		"AppliesToInternal", "AppliesToExternalKnown", "AppliesToExternalUnknown", "Enabled", "ReplyMessage",
		"BodyType", "DevicePassword", "Password", "DeviceInformation", "Model", "IMEI", "FriendlyName", "OS",
		"OSLanguage", "PhoneNumber", "UserInformation", "EmailAddresses", "SmtpAddress", "UserAgent",
		"EnableOutboundSMS", "MobileOperator", "PrimarySmtpAddress", "Accounts", "Account", "AccountId",
		"AccountName", "UserDisplayName", "SendDisabled", "", "RightsManagementInformation",
	}},
	PageDocumentLibrary: {"DocumentLibrary", []string{
		"LinkId", "DisplayName", "IsFolder", "CreationDate", "LastModifiedDate", "IsHidden", "ContentLength",
		"ContentType",
	}},
	PageItemOperations: {"ItemOperations", []string{
		"ItemOperations", "Fetch", "Store", "Options", "Range", "Total", "Properties", "Data", "Status",
		"Response", "Version", "Schema", "Part", "EmptyFolderContents", "DeleteSubFolders", "UserName",
		"Password", "Move", "DstFldId", "ConversationId", "MoveAlways",
	}},
	PageComposeMail: {"ComposeMail", []string{
		"SendMail", "SmartForward", "SmartReply", "SaveInSentItems", "ReplaceMime", "", "Source", "FolderId",
		"ItemId", "LongId", "InstanceId", "Mime", "ClientId", "Status", "AccountId", "", "Forwardees",
		"Forwardee", "ForwardeeName", "ForwardeeEmail",
	}},
	PageEmail2: {"Email2", []string{
		"UmCallerID", "UmUserNotes", "UmAttDuration", "UmAttOrder", "ConversationId", "ConversationIndex",
		"LastVerbExecuted", "LastVerbExecutionTime", "ReceivedAsBcc", "Sender", "CalendarType", "IsLeapMonth",
		"AccountId", "FirstDayOfWeek", "MeetingMessageType", "", "IsDraft", "Bcc", "Send",
	}},
	PageNotes: {"Notes", []string{
		"Subject", "MessageClass", "LastModifiedDate", "Categories", "Category",
	}},
	PageRightsManagement: {"RightsManagement", []string{
		"RightsManagementSupport", "RightsManagementTemplates", "RightsManagementTemplate",
		"RightsManagementLicense", "EditAllowed", "ReplyAllowed", "ReplyAllAllowed", "ForwardAllowed",
		"ModifyRecipientsAllowed", "ExtractAllowed", "PrintAllowed", "ExportAllowed",
		"ProgrammaticAccessAllowed", "Owner", "ContentExpiryDate", "TemplateID", "TemplateName",
		"TemplateDescription", "ContentOwner", "RemoveRightsManagementProtection",
	}},
}

// tagsByName maps "Page:Tag" to its token.
var tagsByName = func() map[string]Tag {
	m := make(map[string]Tag, 700)
	for p, cp := range codePages {
		for i, name := range cp.tags {
			if name == "" {
				continue
			}
			m[cp.name+":"+name] = MakeTag(byte(p), byte(firstToken+i))
		}
	}
	return m
}()

// NumPages is the number of known code pages.
const NumPages = len(codePages)

// PageName returns the name of a code page ("" if unknown).
func PageName(page byte) string {
	if int(page) < len(codePages) {
		return codePages[page].name
	}
	return ""
}
