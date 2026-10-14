package extraction

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// fixtureFile mirrors testdata/documents.json: a document, the answer a model
// returned for it, and the audited outcome a reviewer should see.
//
// The model answer is part of the fixture on purpose. No live model runs in this
// suite, so the only honest way to test the validation layer against realistic
// input is to freeze what the model said alongside the text it said it about.
type fixtureFile struct {
	PolicyVersion string        `json:"policyVersion"`
	Cases         []fixtureCase `json:"cases"`
}

type fixtureCase struct {
	Name        string          `json:"name"`
	DocumentID  string          `json:"documentId"`
	MIMEType    string          `json:"mimeType"`
	OCRText     string          `json:"ocrText"`
	ModelAnswer candidateAnswer `json:"modelAnswer"`
	Expect      fixtureExpect   `json:"expect"`
}

type fixtureExpect struct {
	Values         map[string]string `json:"values"`
	Absent         []string          `json:"absent"`
	IssueCodes     []string          `json:"issueCodes"`
	ReviewRequired bool              `json:"reviewRequired"`
	EvidencePages  map[string]int    `json:"evidencePages"`
}

func loadFixtures(t *testing.T) fixtureFile {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/documents.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var file fixtureFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("the fixture file carries no cases")
	}
	return file
}

// TestEvaluateAgainstDocumentedFixtures drives the rules layer with the frozen
// cases and checks the outcome a reviewer would act on.
//
// It also re-checks the central promise from outside the implementation: every
// reported field must point at characters that really exist in the OCR text, and
// the reported span must select exactly the quote it carries. A field that
// survived processing without that property fails here even if the layer
// believes it succeeded.
func TestEvaluateAgainstDocumentedFixtures(t *testing.T) {
	file := loadFixtures(t)
	policy := DefaultPolicy()

	if file.PolicyVersion != policy.Version {
		t.Fatalf("fixtures were written for policy %q but DefaultPolicy is %q",
			file.PolicyVersion, policy.Version)
	}

	for _, tc := range file.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			in := DocumentInput{DocumentID: tc.DocumentID, MIMEType: tc.MIMEType, OCRText: tc.OCRText}
			got := Evaluate(in, tc.ModelAnswer, policy)

			if len(got.Fields) != len(tc.Expect.Values) {
				var names []string
				for _, f := range got.Fields {
					names = append(names, f.Name)
				}
				t.Fatalf("got %d fields %v, want %d", len(got.Fields), names, len(tc.Expect.Values))
			}

			for name, want := range tc.Expect.Values {
				field, ok := got.Field(name)
				if !ok {
					t.Errorf("field %q is missing", name)
					continue
				}
				if field.Value != want {
					t.Errorf("field %q = %q, want %q", name, field.Value, want)
				}
			}
			for _, name := range tc.Expect.Absent {
				if _, ok := got.Field(name); ok {
					t.Errorf("field %q should not have been reported", name)
				}
			}

			if gotCodes := sortedCodes(got.Errors); !sameSet(gotCodes, tc.Expect.IssueCodes) {
				t.Errorf("issue codes = %v, want %v", gotCodes, tc.Expect.IssueCodes)
			}

			if got.Review.Required != tc.Expect.ReviewRequired {
				t.Errorf("review required = %v, want %v (reasons %v)",
					got.Review.Required, tc.Expect.ReviewRequired, got.Review.Reasons)
			}
			if got.Review.Required && len(got.Review.Reasons) == 0 {
				t.Error("review was required but no reason was given")
			}

			for name, wantPage := range tc.Expect.EvidencePages {
				field, ok := got.Field(name)
				if !ok {
					t.Errorf("field %q is missing, cannot check its page", name)
					continue
				}
				if field.Evidence.Page != wantPage {
					t.Errorf("field %q evidence page = %d, want %d", name, field.Evidence.Page, wantPage)
				}
			}

			// The independent half: check the promise against the OCR text
			// itself rather than against the layer's own bookkeeping.
			runes := []rune(tc.OCRText)
			for _, f := range got.Fields {
				if !strings.Contains(tc.OCRText, f.Evidence.Quote) {
					t.Errorf("field %q quotes %q, which does not appear in the OCR text",
						f.Name, f.Evidence.Quote)
				}
				if f.Evidence.Start < 0 || f.Evidence.End > len(runes) || f.Evidence.End <= f.Evidence.Start {
					t.Errorf("field %q has an out of range span [%d:%d]", f.Name, f.Evidence.Start, f.Evidence.End)
					continue
				}
				if span := string(runes[f.Evidence.Start:f.Evidence.End]); span != f.Evidence.Quote {
					t.Errorf("field %q span [%d:%d] = %q, want the quote %q",
						f.Name, f.Evidence.Start, f.Evidence.End, span, f.Evidence.Quote)
				}
				if f.Confidence < 0 || f.Confidence > 1 {
					t.Errorf("field %q confidence = %v, outside [0,1]", f.Name, f.Confidence)
				}
			}

			if issues := got.Validate(); len(issues) != 0 {
				t.Errorf("the layer produced an extraction that fails its own shape check: %v", issues)
			}
		})
	}
}

// TestEvaluateReportsInputProblemsAndExtractsNothing checks that an input this
// slice cannot read produces no fields at all, even when the model answered
// confidently. Extracting something from an image is exactly the claim the
// repository refuses to make.
func TestEvaluateReportsInputProblemsAndExtractsNothing(t *testing.T) {
	in := DocumentInput{DocumentID: "d-1", MIMEType: MIMEImagePNG}
	answer := candidateAnswer{Fields: []candidateField{
		{Name: FieldTotal, Value: "10.00", Quote: "Total: 10.00", Confidence: 0.99},
	}}

	got := Evaluate(in, answer, DefaultPolicy())

	if len(got.Fields) != 0 {
		t.Errorf("got %d fields from an image input, want none", len(got.Fields))
	}
	if want := []string{CodeMissingOCRText, CodeUnsupportedMIMEType}; !sameSet(sortedCodes(got.Errors), want) {
		t.Errorf("issue codes = %v, want %v", sortedCodes(got.Errors), want)
	}
	if !got.Review.Required {
		t.Error("an unreadable input did not require review")
	}
}

// TestEvaluateRejectsDuplicateUnnamedAndUnlocatableCandidates exercises the
// candidate level checks in isolation, with a policy that requires nothing and a
// text whose amount label appears exactly once, so that the reported codes are
// only about the candidates themselves.
func TestEvaluateRejectsDuplicateUnnamedAndUnlocatableCandidates(t *testing.T) {
	in := DocumentInput{
		DocumentID: "d-1",
		MIMEType:   MIMETextPlain,
		OCRText:    "Total: 10.00\n",
	}
	policy := Policy{Version: "1.0.0", ReviewBelow: 0.80}
	answer := candidateAnswer{Fields: []candidateField{
		{Name: FieldTotal, Value: "10.00", Quote: "Total: 10.00", Confidence: 0.90},
		{Name: FieldTotal, Value: "20.00", Quote: "Total: 10.00", Confidence: 0.90},
		{Name: "   ", Value: "x", Quote: "Total: 10.00", Confidence: 0.90},
		{Name: FieldCurrency, Value: "USD", Quote: "Moneda: USD", Confidence: 0.90},
	}}

	got := Evaluate(in, answer, policy)

	if len(got.Fields) != 1 {
		t.Fatalf("got %d fields, want exactly the first total", len(got.Fields))
	}
	if got.Fields[0].Value != "10.00" {
		t.Errorf("kept value = %q, want %q", got.Fields[0].Value, "10.00")
	}
	want := []string{CodeDuplicateField, CodeUnknownField, CodeEvidenceNotFound}
	if gotCodes := sortedCodes(got.Errors); !sameSet(gotCodes, want) {
		t.Errorf("issue codes = %v, want %v", gotCodes, want)
	}
	if !got.Review.Required {
		t.Error("a run with errors did not require review")
	}
}

// TestNormalizeAmountReadsBothSeparatorConventions is the table that decides how
// a printed amount becomes a canonical value. The evidence quote keeps the
// document's own characters; only the value is canonicalised.
func TestNormalizeAmountReadsBothSeparatorConventions(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"1,240.00", "1240.00"},
		{"1.240,00", "1240.00"},
		{"260.40", "260.40"},
		{"260,40", "260.40"},
		{"1,240", "1240.00"},
		{"12.345", "12345.00"},
		{"1.234.567,89", "1234567.89"},
		{"1,234,567.89", "1234567.89"},
		{"1500.40", "1500.40"},
		{"Subtotal: 1,240.00", "1240.00"},
		{"IVA (21%): 260,40", "260.40"},
		{"$1,240.00", "1240.00"},
		{"0.5", "0.50"},
		{"-25.00", "-25.00"},
		{"7", "7.00"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := normalizeAmount(tc.in)
			if !ok {
				t.Fatalf("normalizeAmount(%q) was rejected, want %q", tc.in, tc.want)
			}
			if got != tc.want {
				t.Errorf("normalizeAmount(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeAmountRejectsWhatItCannotRead checks that an amount it cannot
// read with certainty is refused rather than guessed. "1,2,3" is not a number
// under any convention, and "1.2345" carries more precision than this slice
// claims to represent.
func TestNormalizeAmountRejectsWhatItCannotRead(t *testing.T) {
	for _, in := range []string{
		"",
		"abc",
		"USD",
		"1,2,3",
		"1,23,456.78",
		"1.2345",
		"1.",
		"Total:",
	} {
		t.Run(in, func(t *testing.T) {
			if got, ok := normalizeAmount(in); ok {
				t.Errorf("normalizeAmount(%q) = %q, true, want not readable", in, got)
			}
		})
	}
}

// TestNormalizeDateReadsTheDocumentedFormats pins the one date convention this
// slice accepts and refuses a date that does not exist on the calendar.
func TestNormalizeDateReadsTheDocumentedFormats(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"15/03/2026", "2026-03-15", true},
		{"02/04/2026", "2026-04-02", true},
		{"Fecha: 20/06/2026", "2026-06-20", true},
		{"02-04-2026", "2026-04-02", true},
		{"02.04.2026", "2026-04-02", true},
		{"2026-03-15", "2026-03-15", true},
		{"30/02/2026", "", false},
		{"13/13/2026", "", false},
		{"nope", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := normalizeDate(tc.in)
			if ok != tc.ok {
				t.Fatalf("normalizeDate(%q) ok = %v, want %v (got %q)", tc.in, ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Errorf("normalizeDate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEvaluateFlagsARepeatedAmountLabelFromTheTextAlone checks the S8 case the
// value comparison cannot catch: the text names the same total twice, so the
// reader cannot tell which one the document means.
func TestEvaluateFlagsARepeatedAmountLabelFromTheTextAlone(t *testing.T) {
	in := DocumentInput{
		DocumentID: "d-1",
		MIMEType:   MIMETextPlain,
		OCRText:    "Subtotal: 100.00\nTotal: 121.00\nTotal anterior: 500.00\n",
	}
	policy := Policy{Version: "1.0.0", ReviewBelow: 0.80}
	answer := candidateAnswer{Fields: []candidateField{
		{Name: FieldTotal, Value: "121.00", Quote: "Total: 121.00", Confidence: 0.95},
	}}

	got := Evaluate(in, answer, policy)

	want := []string{CodeRepeatedValue}
	if gotCodes := sortedCodes(got.Errors); !sameSet(gotCodes, want) {
		t.Fatalf("issue codes = %v, want %v", gotCodes, want)
	}
	if got.Errors[0].Severity != SeverityWarning {
		t.Errorf("repeated label severity = %q, want %q", got.Errors[0].Severity, SeverityWarning)
	}
	if !got.Review.Required {
		t.Error("a repeated label did not require review")
	}
}

func sortedCodes(issues []Issue) []string {
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	sort.Strings(codes)
	return codes
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	first := append([]string(nil), a...)
	second := append([]string(nil), b...)
	sort.Strings(first)
	sort.Strings(second)
	for i := range first {
		if first[i] != second[i] {
			return false
		}
	}
	return true
}
