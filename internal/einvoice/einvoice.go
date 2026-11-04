// SPDX-License-Identifier: AGPL-3.0-or-later

// Package einvoice detects and reads structured electronic invoices
// (KONZEPT 8.11): ZUGFeRD / Factur-X (PDF/A-3 with an embedded CII XML,
// factur-x.xml, zugferd-invoice.xml, xrechnung.xml) and XRechnung as UN/CEFACT
// CII or OASIS UBL XML attachments. It extracts invoice number, issue date,
// due date, amount, currency, seller and IBAN.
package einvoice

import (
	"bytes"
	"compress/zlib"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Formats.
const (
	FormatFacturX  = "facturx"  // ZUGFeRD 2.x / Factur-X PDF with CII
	FormatZUGFeRD1 = "zugferd1" // ZUGFeRD 1.0 PDF
	FormatCII      = "cii"      // CII XML attachment (XRechnung CII)
	FormatUBL      = "ubl"      // UBL XML attachment (XRechnung UBL)
)

// Invoice is the extracted invoice data.
type Invoice struct {
	Format   string    `json:"format"`
	Profile  string    `json:"profile,omitempty"` // guideline id, e.g. urn:cen.eu:en16931:2017#compliant#urn:xeinkauf.de:kosit:xrechnung_3.0
	Number   string    `json:"number,omitempty"`
	Type     string    `json:"type,omitempty"` // 380 invoice, 381 credit note ...
	Issued   time.Time `json:"issued,omitzero"`
	Due      time.Time `json:"due,omitzero"`
	Amount   string    `json:"amount,omitempty"` // decimal string as in the XML
	Currency string    `json:"currency,omitempty"`
	Seller   string    `json:"seller,omitempty"`
	Buyer    string    `json:"buyer,omitempty"`
	IBAN     string    `json:"iban,omitempty"`
	BIC      string    `json:"bic,omitempty"`
	// Reference is the payment reference (remittance information).
	Reference string `json:"reference,omitempty"`
	// Source is the attachment the data came from.
	Source string `json:"source,omitempty"`
}

// IsXRechnung reports whether the profile is an XRechnung profile.
func (i Invoice) IsXRechnung() bool { return strings.Contains(strings.ToLower(i.Profile), "xrechnung") }

// AmountFloat parses Amount (0 if empty or invalid).
func (i Invoice) AmountFloat() float64 {
	f, _ := strconv.ParseFloat(i.Amount, 64)
	return f
}

// Attachment is a mail attachment to inspect.
type Attachment struct {
	Name string
	MIME string
	Data []byte
}

// ErrNoInvoice is returned when no structured invoice was found.
var ErrNoInvoice = errors.New("einvoice: no structured invoice")

// Detect inspects the attachments and returns the first structured
// invoice. XML attachments win over PDFs (XRechnung is the legal original).
func Detect(atts []Attachment) (*Invoice, error) {
	for _, a := range atts {
		if isXML(a) {
			if inv, err := ParseXML(a.Data); err == nil {
				inv.Source = a.Name
				return inv, nil
			}
		}
	}
	for _, a := range atts {
		if isPDF(a) {
			if inv, err := FromPDF(a.Data); err == nil {
				inv.Source = a.Name
				return inv, nil
			}
		}
	}
	return nil, ErrNoInvoice
}

func isXML(a Attachment) bool {
	n, m := strings.ToLower(a.Name), strings.ToLower(a.MIME)
	return strings.HasSuffix(n, ".xml") || strings.Contains(m, "xml")
}

func isPDF(a Attachment) bool {
	return strings.HasSuffix(strings.ToLower(a.Name), ".pdf") || strings.EqualFold(a.MIME, "application/pdf") ||
		bytes.HasPrefix(a.Data, []byte("%PDF"))
}

// ==== PDF ========================================================================

var (
	streamRe       = regexp.MustCompile(`(?s)stream\r?\n`)
	embeddedNameRe = regexp.MustCompile(`(?i)(factur-x\.xml|zugferd-invoice\.xml|zugferd_invoice\.xml|xrechnung\.xml|ZUGFeRD-invoice\.xml)`)
)

// FromPDF extracts the embedded invoice XML of a ZUGFeRD/Factur-X PDF.
// Every stream is inspected (inflated if needed), which also covers PDFs
// whose file specification sits in a compressed object stream.
func FromPDF(pdf []byte) (*Invoice, error) {
	if !bytes.HasPrefix(bytes.TrimLeft(pdf, "\r\n\t "), []byte("%PDF")) {
		return nil, ErrNoInvoice
	}
	named := embeddedNameRe.Match(pdf)
	var found *Invoice
	pdfStreams(pdf, func(xmlData []byte) bool {
		if !bytes.Contains(xmlData, []byte("CrossIndustry")) && !bytes.Contains(xmlData, []byte("Invoice")) {
			return true
		}
		inv, err := ParseXML(xmlData)
		if err != nil {
			return true
		}
		switch inv.Format {
		case FormatCII:
			inv.Format = FormatFacturX
		case formatCII1:
			inv.Format = FormatZUGFeRD1
		}
		found = inv
		return false
	})
	if found != nil {
		return found, nil
	}
	if named {
		return nil, errors.New("einvoice: PDF names an invoice XML but it could not be read")
	}
	return nil, ErrNoInvoice
}

// Limits for hostile attachments (audit B-10): a PDF may contain thousands
// of small Flate "bombs", an XML document millions of nested elements.
const (
	maxStreamBytes   = 16 << 20 // one inflated stream
	maxInflatedTotal = 64 << 20 // all inflated streams of a PDF
	maxXMLBytes      = 16 << 20
	maxXMLDepth      = 64
	maxXMLElements   = 100000
)

// pdfStreams calls fn with the (decoded) contents of the streams that look
// like markup, until fn returns false or the inflate budget is used up.
func pdfStreams(pdf []byte, fn func([]byte) bool) {
	budget := int64(maxInflatedTotal)
	locs := streamRe.FindAllIndex(pdf, -1)
	for _, loc := range locs {
		start := loc[1]
		end := bytes.Index(pdf[start:], []byte("endstream"))
		if end < 0 {
			continue
		}
		data := bytes.TrimRight(pdf[start:start+end], "\r\n")
		// Dictionary before "stream": look back for FlateDecode.
		dictStart := bytes.LastIndex(pdf[:loc[0]], []byte("obj"))
		flate := dictStart >= 0 && bytes.Contains(pdf[dictStart:loc[0]], []byte("FlateDecode"))
		if flate {
			if budget <= 0 {
				return
			}
			zr, err := zlib.NewReader(bytes.NewReader(data))
			if err != nil {
				continue
			}
			dec, err := io.ReadAll(io.LimitReader(zr, min(budget, maxStreamBytes)))
			budget -= int64(len(dec))
			if err != nil && len(dec) == 0 {
				continue
			}
			data = dec
		}
		if bytes.Contains(data, []byte("<")) && !fn(data) {
			return
		}
	}
}

// ==== XML ========================================================================

const formatCII1 = "cii1"

type elem struct {
	path  []string // local names from the root
	attrs map[string]string
	text  string
}

// ParseXML reads a CII (ZUGFeRD 1/2, Factur-X, XRechnung CII) or UBL
// (Invoice, CreditNote) document.
func ParseXML(data []byte) (*Invoice, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(data) > maxXMLBytes {
		return nil, ErrNoInvoice
	}
	// encoding/xml never resolves DTDs or external entities (no XXE, no
	// entity expansion); depth and element count are bounded here.
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var stack []string
	var attrStack []map[string]string
	var text strings.Builder
	var elems []elem
	root := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if root == "" {
				return nil, err
			}
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if root == "" {
				root = t.Name.Local
			}
			if len(stack) >= maxXMLDepth || len(elems) >= maxXMLElements {
				if root == "" {
					return nil, ErrNoInvoice
				}
				goto done
			}
			stack = append(stack, t.Name.Local)
			at := map[string]string{}
			for _, a := range t.Attr {
				at[a.Name.Local] = a.Value
			}
			attrStack = append(attrStack, at)
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			elems = append(elems, elem{path: append([]string(nil), stack...), attrs: attrStack[len(attrStack)-1], text: strings.TrimSpace(text.String())})
			stack = stack[:len(stack)-1]
			attrStack = attrStack[:len(attrStack)-1]
			text.Reset()
		}
	}
done:
	switch root {
	case "CrossIndustryInvoice":
		return cii(elems, FormatCII), nil
	case "CrossIndustryDocument":
		return cii(elems, formatCII1), nil
	case "Invoice", "CreditNote":
		return ubl(elems, root), nil
	}
	return nil, ErrNoInvoice
}

// find returns the first element whose path ends with suffix (local names).
func find(elems []elem, suffix ...string) *elem {
	for i := range elems {
		p := elems[i].path
		if len(p) < len(suffix) {
			continue
		}
		ok := true
		for j := range suffix {
			if p[len(p)-len(suffix)+j] != suffix[j] {
				ok = false
				break
			}
		}
		if ok && elems[i].text != "" {
			return &elems[i]
		}
	}
	return nil
}

// findAt is find restricted to a path depth (len(path) == depth).
func findAt(elems []elem, depth int, suffix ...string) *elem {
	for i := range elems {
		if len(elems[i].path) != depth {
			continue
		}
		if e := find(elems[i:i+1], suffix...); e != nil {
			return e
		}
	}
	return nil
}

func txt(e *elem) string {
	if e == nil {
		return ""
	}
	return e.text
}

func cii(elems []elem, format string) *Invoice {
	inv := &Invoice{Format: format}
	inv.Profile = txt(find(elems, "GuidelineSpecifiedDocumentContextParameter", "ID"))
	if format == formatCII1 {
		inv.Number = txt(find(elems, "HeaderExchangedDocument", "ID"))
		inv.Type = txt(find(elems, "HeaderExchangedDocument", "TypeCode"))
		inv.Issued = ciiDate(find(elems, "HeaderExchangedDocument", "IssueDateTime", "DateTimeString"))
	} else {
		inv.Number = txt(find(elems, "ExchangedDocument", "ID"))
		inv.Type = txt(find(elems, "ExchangedDocument", "TypeCode"))
		inv.Issued = ciiDate(find(elems, "ExchangedDocument", "IssueDateTime", "DateTimeString"))
	}
	inv.Due = ciiDate(find(elems, "SpecifiedTradePaymentTerms", "DueDateDateTime", "DateTimeString"))
	inv.Currency = txt(find(elems, "InvoiceCurrencyCode"))
	sum := []string{"SpecifiedTradeSettlementHeaderMonetarySummation"}
	if format == formatCII1 {
		sum = []string{"SpecifiedTradeSettlementMonetarySummation"}
	}
	for _, field := range []string{"DuePayableAmount", "GrandTotalAmount"} {
		if e := find(elems, append(sum, field)...); e != nil {
			inv.Amount = e.text
			if inv.Currency == "" {
				inv.Currency = e.attrs["currencyID"]
			}
			break
		}
	}
	inv.Seller = txt(find(elems, "SellerTradeParty", "Name"))
	inv.Buyer = txt(find(elems, "BuyerTradeParty", "Name"))
	inv.IBAN = normIBAN(txt(find(elems, "PayeePartyCreditorFinancialAccount", "IBANID")))
	inv.BIC = txt(find(elems, "PayeeSpecifiedCreditorFinancialInstitution", "BICID"))
	inv.Reference = txt(find(elems, "ApplicableHeaderTradeSettlement", "PaymentReference"))
	return inv
}

func ciiDate(e *elem) time.Time {
	if e == nil {
		return time.Time{}
	}
	s := strings.TrimSpace(e.text)
	switch e.attrs["format"] {
	case "", "102":
		if t, err := time.Parse("20060102", s); err == nil {
			return t
		}
	case "610":
		if t, err := time.Parse("200601", s); err == nil {
			return t
		}
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return time.Time{}
}

func ubl(elems []elem, root string) *Invoice {
	inv := &Invoice{Format: FormatUBL}
	inv.Profile = txt(findAt(elems, 2, "CustomizationID"))
	inv.Number = txt(findAt(elems, 2, "ID"))
	if root == "CreditNote" {
		inv.Type = txt(findAt(elems, 2, "CreditNoteTypeCode"))
		if inv.Type == "" {
			inv.Type = "381"
		}
	} else {
		inv.Type = txt(findAt(elems, 2, "InvoiceTypeCode"))
	}
	inv.Issued = isoDate(txt(findAt(elems, 2, "IssueDate")))
	inv.Due = isoDate(txt(findAt(elems, 2, "DueDate")))
	if inv.Due.IsZero() {
		inv.Due = isoDate(txt(find(elems, "PaymentMeans", "PaymentDueDate")))
	}
	inv.Currency = txt(findAt(elems, 2, "DocumentCurrencyCode"))
	if e := find(elems, "LegalMonetaryTotal", "PayableAmount"); e != nil {
		inv.Amount = e.text
		if inv.Currency == "" {
			inv.Currency = e.attrs["currencyID"]
		}
	}
	inv.Seller = txt(find(elems, "AccountingSupplierParty", "Party", "PartyName", "Name"))
	if inv.Seller == "" {
		inv.Seller = txt(find(elems, "AccountingSupplierParty", "Party", "PartyLegalEntity", "RegistrationName"))
	}
	inv.Buyer = txt(find(elems, "AccountingCustomerParty", "Party", "PartyName", "Name"))
	if inv.Buyer == "" {
		inv.Buyer = txt(find(elems, "AccountingCustomerParty", "Party", "PartyLegalEntity", "RegistrationName"))
	}
	inv.IBAN = normIBAN(txt(find(elems, "PaymentMeans", "PayeeFinancialAccount", "ID")))
	inv.BIC = txt(find(elems, "PayeeFinancialAccount", "FinancialInstitutionBranch", "ID"))
	inv.Reference = txt(find(elems, "PaymentMeans", "PaymentID"))
	return inv
}

func isoDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

func normIBAN(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
}

// ValidIBAN checks the ISO 13616 checksum.
func ValidIBAN(iban string) bool {
	s := normIBAN(iban)
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	s = s[4:] + s[:4]
	rem := 0
	for _, r := range s {
		var v int
		switch {
		case r >= '0' && r <= '9':
			v = int(r - '0')
			rem = (rem*10 + v) % 97
		case r >= 'A' && r <= 'Z':
			v = int(r-'A') + 10
			rem = (rem*100 + v) % 97
		default:
			return false
		}
	}
	return rem == 1
}
