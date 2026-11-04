// SPDX-License-Identifier: AGPL-3.0-or-later

package einvoice

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"testing"
	"time"
)

const ciiXML = `<?xml version="1.0" encoding="UTF-8"?>
<rsm:CrossIndustryInvoice xmlns:rsm="urn:un:unece:uncefact:data:standard:CrossIndustryInvoice:100"
  xmlns:ram="urn:un:unece:uncefact:data:standard:ReusableAggregateBusinessInformationEntity:100"
  xmlns:udt="urn:un:unece:uncefact:data:standard:UnqualifiedDataType:100">
  <rsm:ExchangedDocumentContext>
    <ram:GuidelineSpecifiedDocumentContextParameter><ram:ID>urn:cen.eu:en16931:2017#compliant#urn:xeinkauf.de:kosit:xrechnung_3.0</ram:ID></ram:GuidelineSpecifiedDocumentContextParameter>
  </rsm:ExchangedDocumentContext>
  <rsm:ExchangedDocument>
    <ram:ID>RE-2026-0042</ram:ID><ram:TypeCode>380</ram:TypeCode>
    <ram:IssueDateTime><udt:DateTimeString format="102">20261015</udt:DateTimeString></ram:IssueDateTime>
  </rsm:ExchangedDocument>
  <rsm:SupplyChainTradeTransaction>
    <ram:ApplicableHeaderTradeAgreement>
      <ram:SellerTradeParty><ram:Name>Muster Lieferant GmbH</ram:Name></ram:SellerTradeParty>
      <ram:BuyerTradeParty><ram:Name>Kunde AG</ram:Name></ram:BuyerTradeParty>
    </ram:ApplicableHeaderTradeAgreement>
    <ram:ApplicableHeaderTradeSettlement>
      <ram:PaymentReference>RE-2026-0042</ram:PaymentReference>
      <ram:InvoiceCurrencyCode>EUR</ram:InvoiceCurrencyCode>
      <ram:SpecifiedTradeSettlementPaymentMeans><ram:TypeCode>58</ram:TypeCode>
        <ram:PayeePartyCreditorFinancialAccount><ram:IBANID>DE89 3704 0044 0532 0130 00</ram:IBANID></ram:PayeePartyCreditorFinancialAccount>
      </ram:SpecifiedTradeSettlementPaymentMeans>
      <ram:SpecifiedTradePaymentTerms><ram:DueDateDateTime><udt:DateTimeString format="102">20261029</udt:DateTimeString></ram:DueDateDateTime></ram:SpecifiedTradePaymentTerms>
      <ram:SpecifiedTradeSettlementHeaderMonetarySummation>
        <ram:GrandTotalAmount>119.00</ram:GrandTotalAmount>
        <ram:DuePayableAmount>119.00</ram:DuePayableAmount>
      </ram:SpecifiedTradeSettlementHeaderMonetarySummation>
    </ram:ApplicableHeaderTradeSettlement>
  </rsm:SupplyChainTradeTransaction>
</rsm:CrossIndustryInvoice>`

const ublXML = `<?xml version="1.0" encoding="UTF-8"?>
<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"
  xmlns:cac="urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
  xmlns:cbc="urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2">
  <cbc:CustomizationID>urn:cen.eu:en16931:2017#compliant#urn:xeinkauf.de:kosit:xrechnung_3.0</cbc:CustomizationID>
  <cbc:ID>4711</cbc:ID>
  <cbc:IssueDate>2026-09-01</cbc:IssueDate>
  <cbc:DueDate>2026-09-15</cbc:DueDate>
  <cbc:InvoiceTypeCode>380</cbc:InvoiceTypeCode>
  <cbc:DocumentCurrencyCode>EUR</cbc:DocumentCurrencyCode>
  <cac:AccountingSupplierParty><cac:Party><cac:PartyLegalEntity><cbc:RegistrationName>Stadtwerke Beispiel</cbc:RegistrationName></cac:PartyLegalEntity></cac:Party></cac:AccountingSupplierParty>
  <cac:AccountingCustomerParty><cac:Party><cac:PartyName><cbc:Name>Max Mustermann</cbc:Name></cac:PartyName></cac:Party></cac:AccountingCustomerParty>
  <cac:PaymentMeans><cbc:PaymentMeansCode>58</cbc:PaymentMeansCode><cbc:PaymentID>K-99</cbc:PaymentID>
    <cac:PayeeFinancialAccount><cbc:ID>DE02120300000000202051</cbc:ID></cac:PayeeFinancialAccount></cac:PaymentMeans>
  <cac:InvoiceLine><cbc:ID>1</cbc:ID></cac:InvoiceLine>
  <cac:LegalMonetaryTotal><cbc:PayableAmount currencyID="EUR">58.31</cbc:PayableAmount></cac:LegalMonetaryTotal>
</Invoice>`

func day(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }

func TestCII(t *testing.T) {
	inv, err := ParseXML([]byte(ciiXML))
	if err != nil {
		t.Fatal(err)
	}
	want := Invoice{Format: FormatCII, Number: "RE-2026-0042", Type: "380", Issued: day("2026-10-15"), Due: day("2026-10-29"),
		Amount: "119.00", Currency: "EUR", Seller: "Muster Lieferant GmbH", Buyer: "Kunde AG", IBAN: "DE89370400440532013000", Reference: "RE-2026-0042"}
	want.Profile = inv.Profile
	if *inv != want || !inv.IsXRechnung() || !ValidIBAN(inv.IBAN) {
		t.Fatalf("got %+v", inv)
	}
}

func TestUBL(t *testing.T) {
	inv, err := Detect([]Attachment{{Name: "readme.txt", Data: []byte("x")}, {Name: "rechnung.xml", MIME: "application/xml", Data: []byte(ublXML)}})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Format != FormatUBL || inv.Number != "4711" || inv.Amount != "58.31" || inv.Currency != "EUR" || inv.Seller != "Stadtwerke Beispiel" ||
		inv.IBAN != "DE02120300000000202051" || !inv.Due.Equal(day("2026-09-15")) || inv.Source != "rechnung.xml" || inv.Reference != "K-99" {
		t.Fatalf("got %+v", inv)
	}
	if inv.AmountFloat() != 58.31 {
		t.Fatal("amount")
	}
}

// minimalPDF builds a small PDF with a compressed embedded factur-x.xml.
func minimalPDF(xml string) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write([]byte(xml))
	zw.Close()
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R /Names << /EmbeddedFiles << /Names [(factur-x.xml) 4 0 R] >> >> >>\nendobj\n")
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>\nendobj\n")
	b.WriteString("4 0 obj\n<< /Type /Filespec /F (factur-x.xml) /UF (factur-x.xml) /AFRelationship /Alternative /EF << /F 5 0 R >> >>\nendobj\n")
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /EmbeddedFile /Subtype /text#2Fxml /Filter /FlateDecode /Length %d >>\nstream\n", z.Len())
	b.Write(z.Bytes())
	b.WriteString("\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return b.Bytes()
}

func TestFacturXPDF(t *testing.T) {
	inv, err := Detect([]Attachment{{Name: "Rechnung.pdf", MIME: "application/pdf", Data: minimalPDF(ciiXML)}})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Format != FormatFacturX || inv.Number != "RE-2026-0042" || inv.Amount != "119.00" {
		t.Fatalf("got %+v", inv)
	}
	if _, err := Detect([]Attachment{{Name: "plain.pdf", Data: []byte("%PDF-1.4\n1 0 obj<<>>endobj\n")}}); err != ErrNoInvoice {
		t.Fatalf("plain pdf: %v", err)
	}
	if ValidIBAN("DE89370400440532013001") {
		t.Fatal("bad iban accepted")
	}
}
