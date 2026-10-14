package extraction

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDocumentInputValidateNamesTheDeferredScope checks that the media types the
// slice cannot handle are rejected with an honest reason instead of a generic
// format error, and that an OCR text input is accepted untouched.
func TestDocumentInputValidateNamesTheDeferredScope(t *testing.T) {
	tests := []struct {
		name       string
		input      DocumentInput
		wantCodes  []string
		wantInMsgs []string
	}{
		{
			name:  "plain text is accepted",
			input: DocumentInput{DocumentID: "d-1", MIMEType: MIMETextPlain, OCRText: "Total $10.00"},
		},
		{
			name:       "an image is out of scope and says so",
			input:      DocumentInput{DocumentID: "d-1", MIMEType: MIMEImagePNG, OCRText: "Total $10.00"},
			wantCodes:  []string{CodeUnsupportedMIMEType},
			wantInMsgs: []string{"no image loader and no OCR engine here", MIMETextPlain},
		},
		{
			name:       "a pdf is out of scope and says so",
			input:      DocumentInput{DocumentID: "d-1", MIMEType: MIMEApplicationPDF, OCRText: "Total $10.00"},
			wantCodes:  []string{CodeUnsupportedMIMEType},
			wantInMsgs: []string{"no image loader and no OCR engine here"},
		},
		{
			name:       "a missing media type is named, not guessed",
			input:      DocumentInput{DocumentID: "d-1", OCRText: "Total $10.00"},
			wantCodes:  []string{CodeUnsupportedMIMEType},
			wantInMsgs: []string{"mimeType is required"},
		},
		{
			name:       "empty OCR text cannot be extracted",
			input:      DocumentInput{DocumentID: "d-1", MIMEType: MIMETextPlain, OCRText: "   \n\t "},
			wantCodes:  []string{CodeMissingOCRText},
			wantInMsgs: []string{"never reads a scan"},
		},
		{
			name:       "identity is required",
			input:      DocumentInput{DocumentID: "  ", MIMEType: MIMETextPlain, OCRText: "Total $10.00"},
			wantCodes:  []string{CodeMissingDocumentID},
			wantInMsgs: []string{"documentId is required"},
		},
		{
			name:       "identity and text are reported together",
			input:      DocumentInput{},
			wantCodes:  []string{CodeMissingDocumentID, CodeUnsupportedMIMEType, CodeMissingOCRText},
			wantInMsgs: []string{"documentId is required", "mimeType is required", "ocrText is required"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issues := tc.input.Validate()
			if len(issues) != len(tc.wantCodes) {
				t.Fatalf("got %d issues (%v), want %d", len(issues), issues, len(tc.wantCodes))
			}
			for i, want := range tc.wantCodes {
				if issues[i].Code != want {
					t.Errorf("issue %d code = %q, want %q", i, issues[i].Code, want)
				}
				if issues[i].Severity != SeverityError {
					t.Errorf("issue %d severity = %q, want %q", i, issues[i].Severity, SeverityError)
				}
				if issues[i].Message == "" {
					t.Errorf("issue %d has an empty message", i)
				}
			}
			joined := ""
			for _, issue := range issues {
				joined += issue.Message + "\n"
			}
			for _, want := range tc.wantInMsgs {
				if !strings.Contains(joined, want) {
					t.Errorf("messages %q do not contain %q", joined, want)
				}
			}
		})
	}
}

// TestExtractionValidateRejectsFieldsWithoutRealEvidence is the structural half
// of the "no invented values" claim: a field that cannot point at a span of the
// OCR text is not a field.
func TestExtractionValidateRejectsFieldsWithoutRealEvidence(t *testing.T) {
	good := Field{
		Name:       FieldTotal,
		Value:      "10.00",
		Confidence: 0.95,
		Evidence:   Evidence{Quote: "Total $10.00", Page: 1, Line: 3, Start: 0, End: 12, Match: MatchExact},
	}

	tests := []struct {
		name  string
		field Field
		want  string
	}{
		{
			name:  "no quote",
			field: Field{Name: FieldTotal, Value: "10.00", Confidence: 0.95},
			want:  CodeEvidenceNotFound,
		},
		{
			name: "a zero-width span is not a location",
			field: Field{Name: FieldTotal, Value: "10.00", Confidence: 0.95,
				Evidence: Evidence{Quote: "Total", Page: 1, Line: 3, Start: 4, End: 4, Match: MatchExact}},
			want: CodeEvidenceNotFound,
		},
		{
			name: "a reversed span is not a location",
			field: Field{Name: FieldTotal, Value: "10.00", Confidence: 0.95,
				Evidence: Evidence{Quote: "Total", Page: 1, Line: 3, Start: 9, End: 2, Match: MatchExact}},
			want: CodeEvidenceNotFound,
		},
		{
			name: "an unknown match kind is refused",
			field: Field{Name: FieldTotal, Value: "10.00", Confidence: 0.95,
				Evidence: Evidence{Quote: "Total $10.00", Page: 1, Line: 3, Start: 0, End: 12, Match: "guessed"}},
			want: CodeEvidenceNotFound,
		},
		{
			name: "confidence above one",
			field: Field{Name: FieldTotal, Value: "10.00", Confidence: 1.5,
				Evidence: Evidence{Quote: "Total $10.00", Page: 1, Line: 3, Start: 0, End: 12, Match: MatchExact}},
			want: CodeInvalidConfidence,
		},
		{
			name: "an empty value is not a value",
			field: Field{Name: FieldTotal, Value: "", Confidence: 0.95,
				Evidence: Evidence{Quote: "Total $10.00", Page: 1, Line: 3, Start: 0, End: 12, Match: MatchExact}},
			want: CodeValueNotInEvidence,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Extraction{DocumentID: "d-1", Fields: []Field{tc.field}}.Validate()
			if len(got) != 1 {
				t.Fatalf("got %d issues (%v), want exactly 1", len(got), got)
			}
			if got[0].Code != tc.want {
				t.Errorf("code = %q, want %q", got[0].Code, tc.want)
			}
			if got[0].Field != tc.field.Name && got[0].Field != "" {
				t.Errorf("issue field = %q, want %q or empty", got[0].Field, tc.field.Name)
			}
		})
	}

	if issues := (Extraction{DocumentID: "d-1", Fields: []Field{good}}).Validate(); len(issues) != 0 {
		t.Fatalf("the well formed extraction was rejected: %v", issues)
	}
}

// TestExtractionValidateRejectsDuplicatesAndUnnamedFields checks that a repeated
// field name is reported rather than silently letting the last one win.
func TestExtractionValidateRejectsDuplicatesAndUnnamedFields(t *testing.T) {
	ev := Evidence{Quote: "Total $10.00", Page: 1, Line: 1, Start: 0, End: 12, Match: MatchExact}
	extraction := Extraction{
		DocumentID: "d-1",
		Fields: []Field{
			{Name: FieldTotal, Value: "10.00", Confidence: 0.9, Evidence: ev},
			{Name: FieldTotal, Value: "20.00", Confidence: 0.9, Evidence: ev},
			{Name: "  ", Value: "x", Confidence: 0.9, Evidence: ev},
		},
	}
	issues := extraction.Validate()
	if len(issues) != 2 {
		t.Fatalf("got %d issues (%v), want 2", len(issues), issues)
	}
	if issues[0].Code != CodeDuplicateField || issues[0].Field != FieldTotal {
		t.Errorf("first issue = %+v, want %s on %s", issues[0], CodeDuplicateField, FieldTotal)
	}
	if issues[1].Code != CodeUnknownField {
		t.Errorf("second issue = %+v, want %s", issues[1], CodeUnknownField)
	}

	if _, ok := extraction.Field(FieldTotal); !ok {
		t.Error("Field() did not find a name that is present")
	}
	if _, ok := extraction.Field(FieldCurrency); ok {
		t.Error("Field() reported a name that is absent")
	}

	dirty := Extraction{DocumentID: "d-1", Errors: []Issue{NewIssue(CodeArithmeticMismatch, FieldTotal, "1.00 + 2.00 != 4.00")}}
	if !dirty.HasErrors() {
		t.Error("HasErrors() = false with an error severity issue present")
	}
	clean := Extraction{DocumentID: "d-1", Errors: []Issue{NewWarning(CodeRepeatedValue, FieldTotal, "twice")}}
	if clean.HasErrors() {
		t.Error("HasErrors() = true for a warning only")
	}
}

// TestWireContractKeepsTheDocumentedNames pins the JSON contract the caller
// supplies and the one it receives, so a rename cannot silently break a caller.
func TestWireContractKeepsTheDocumentedNames(t *testing.T) {
	inputJSON := `{"documentId":"d-1","mimeType":"text/plain","ocrText":"Total $10.00"}`
	var input DocumentInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatalf("Unmarshal(input) error = %v", err)
	}
	if input.DocumentID != "d-1" || input.MIMEType != MIMETextPlain || input.OCRText != "Total $10.00" {
		t.Fatalf("round trip lost data: %+v", input)
	}
	// OCR text without a media type must still parse: the field is optional on
	// the wire even though this slice cannot extract without it.
	var withoutText DocumentInput
	if err := json.Unmarshal([]byte(`{"documentId":"d-1","mimeType":"text/plain"}`), &withoutText); err != nil {
		t.Fatalf("Unmarshal(optional ocrText) error = %v", err)
	}
	if len(withoutText.Validate()) != 1 || withoutText.Validate()[0].Code != CodeMissingOCRText {
		t.Fatalf("a text input with no ocrText was not rejected with %s: %v", CodeMissingOCRText, withoutText.Validate())
	}

	extraction := Extraction{
		DocumentID: "d-1",
		Policy:     DefaultPolicyVersion,
		Fields: []Field{{
			Name:       FieldTotal,
			Value:      "10.00",
			Confidence: 0.91,
			Evidence:   Evidence{Quote: "Total $10.00", Page: 1, Line: 3, Start: 0, End: 12, Match: MatchExact},
		}},
		Errors: []Issue{NewWarning(CodeRepeatedValue, FieldTotal, "total appears twice")},
		Review: Review{Required: true, Reasons: []string{"total appears twice"}},
	}
	raw, err := json.Marshal(extraction)
	if err != nil {
		t.Fatalf("Marshal(Extraction) error = %v", err)
	}

	for _, want := range []string{
		`"documentId"`, `"policyVersion"`, `"fields"`, `"name"`, `"value"`,
		`"confidence"`, `"evidence"`, `"quote"`, `"page"`, `"line"`, `"start"`,
		`"end"`, `"match"`, `"errors"`, `"code"`, `"severity"`, `"message"`,
		`"review"`, `"required"`, `"reasons"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("Extraction JSON is missing %s:\n%s", want, raw)
		}
	}
}

// TestCandidateFieldCarriesNoEvidence is the structural guarantee that a model
// answer cannot place its own evidence: the shape it may produce has no member
// for a span, a page or a confidence-bearing coordinate.
func TestCandidateFieldCarriesNoEvidence(t *testing.T) {
	raw, err := json.Marshal(candidateAnswer{Fields: []candidateField{
		{Name: FieldTotal, Value: "10.00", Quote: "Total $10.00", Confidence: 0.9},
	}})
	if err != nil {
		t.Fatalf("Marshal(candidateAnswer) error = %v", err)
	}
	for _, forbidden := range []string{`"evidence"`, `"start"`, `"end"`, `"page"`, `"line"`, `"match"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the model-facing shape carries %s, which Go must derive:\n%s", forbidden, raw)
		}
	}
	for _, wanted := range []string{`"name"`, `"value"`, `"quote"`, `"confidence"`} {
		if !strings.Contains(string(raw), wanted) {
			t.Errorf("the model-facing shape is missing %s:\n%s", wanted, raw)
		}
	}
}

// TestPolicyRejectsSomethingThatWouldSilentlyDisableACheck guards the policy
// itself: a malformed rule must fail loudly instead of switching a check off.
func TestPolicyRejectsSomethingThatWouldSilentlyDisableACheck(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
		want   string
	}{
		{
			name:   "no version",
			policy: Policy{ReviewBelow: 0.8, Required: []string{FieldTotal}},
			want:   CodeMissingDocumentID,
		},
		{
			name:   "impossible threshold",
			policy: Policy{Version: "1.0.0", ReviewBelow: 1.4},
			want:   CodeInvalidConfidence,
		},
		{
			name:   "an arithmetic rule with no addends",
			policy: Policy{Version: "1.0.0", ReviewBelow: 0.8, Arithmetic: []SumRule{{Total: FieldTotal}}},
			want:   CodeArithmeticMismatch,
		},
		{
			name:   "an arithmetic rule with no total",
			policy: Policy{Version: "1.0.0", ReviewBelow: 0.8, Arithmetic: []SumRule{{Addends: []string{FieldSubtotal}}}},
			want:   CodeArithmeticMismatch,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issues := tc.policy.Validate()
			if len(issues) == 0 {
				t.Fatalf("Validate() accepted %+v", tc.policy)
			}
			if issues[0].Code != tc.want {
				t.Errorf("first code = %q, want %q", issues[0].Code, tc.want)
			}
		})
	}

	if issues := DefaultPolicy().Validate(); len(issues) != 0 {
		t.Fatalf("the default policy is not valid: %v", issues)
	}
}

// TestDefaultPolicyShapeMatchesTheDocumentedContract pins what the policy is
// allowed to require, so a later edit cannot quietly demand a new field from
// every document in the fixtures.
func TestDefaultPolicyShapeMatchesTheDocumentedContract(t *testing.T) {
	policy := DefaultPolicy()
	if policy.Version != DefaultPolicyVersion {
		t.Errorf("Version = %q, want %q", policy.Version, DefaultPolicyVersion)
	}
	if policy.ReviewBelow != 0.80 {
		t.Errorf("ReviewBelow = %v, want 0.80", policy.ReviewBelow)
	}
	want := []string{FieldDocumentNumber, FieldIssueDate, FieldCurrency, FieldTotal}
	if len(policy.Required) != len(want) {
		t.Fatalf("Required = %v, want %v", policy.Required, want)
	}
	for i, name := range want {
		if policy.Required[i] != name {
			t.Errorf("Required[%d] = %q, want %q", i, policy.Required[i], name)
		}
	}
	if len(policy.Arithmetic) != 1 {
		t.Fatalf("Arithmetic = %v, want exactly one rule", policy.Arithmetic)
	}
	rule := policy.Arithmetic[0]
	if rule.Total != FieldTotal || len(rule.Addends) != 2 ||
		rule.Addends[0] != FieldSubtotal || rule.Addends[1] != FieldTax {
		t.Errorf("Arithmetic rule = %+v, want subtotal + tax = total", rule)
	}
}

// TestEvidenceRunesCountsWhatTheSpanCovers checks the small accessor the rules
// layer uses to report how much text a field was read from.
func TestEvidenceRunesCountsWhatTheSpanCovers(t *testing.T) {
	ev := Evidence{Quote: "$10.00", Start: 6, End: 12}
	if got := ev.Runes(); got != 6 {
		t.Errorf("Runes() = %d, want 6", got)
	}
}
