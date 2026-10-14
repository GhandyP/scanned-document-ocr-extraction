// Package extraction turns OCR text produced elsewhere into typed document
// fields a human can audit.
//
// The package is text-only on purpose. It does not read a scan, an image or a
// PDF, and it does not run OCR. Everything it knows about a document comes from
// the OCR text the caller supplies, and every field it reports has to point back
// at a real span of that text.
package extraction

import (
	"fmt"
	"strings"
)

// MIMETextPlain is the only media type this slice accepts. The image and PDF
// types are named explicitly so that rejecting one can say what is missing
// instead of failing with a generic format error.
const (
	MIMETextPlain = "text/plain"

	MIMEImagePNG       = "image/png"
	MIMEImageJPEG      = "image/jpeg"
	MIMEApplicationPDF = "application/pdf"
)

// Field names for the invoice shape the default policy describes.
const (
	FieldDocumentNumber = "documentNumber"
	FieldIssueDate      = "issueDate"
	FieldCurrency       = "currency"
	FieldSubtotal       = "subtotal"
	FieldTax            = "tax"
	FieldTotal          = "total"
)

// DefaultPolicyVersion identifies the policy returned by DefaultPolicy.
const DefaultPolicyVersion = "1.0.0"

// DocumentInput is what the caller supplies.
//
// The contract names the OCR text as optional because it anticipates an image
// path, but this slice has no image path: an input with no OCR text cannot be
// extracted and is rejected with CodeMissingOCRText.
type DocumentInput struct {
	DocumentID string `json:"documentId"`
	MIMEType   string `json:"mimeType"`
	OCRText    string `json:"ocrText,omitempty"`
}

// Validate reports every problem that makes an input unusable, so that a run
// fails before any extraction is attempted. Issues are returned in a stable
// order: document identity, then media type, then OCR text.
func (in DocumentInput) Validate() []Issue {
	var issues []Issue

	if strings.TrimSpace(in.DocumentID) == "" {
		issues = append(issues, NewIssue(CodeMissingDocumentID, "", "documentId is required"))
	}

	switch in.MIMEType {
	case MIMETextPlain:
	case "":
		issues = append(issues, NewIssue(CodeUnsupportedMIMEType, "",
			fmt.Sprintf("mimeType is required; this slice accepts %s", MIMETextPlain)))
	case MIMEImagePNG, MIMEImageJPEG, MIMEApplicationPDF:
		issues = append(issues, NewIssue(CodeUnsupportedMIMEType, "",
			fmt.Sprintf("%s is out of scope for this text-only slice: there is no image loader and no OCR engine here, so supply the OCR text as %s instead",
				in.MIMEType, MIMETextPlain)))
	default:
		issues = append(issues, NewIssue(CodeUnsupportedMIMEType, "",
			fmt.Sprintf("%s is not supported; this slice accepts %s", in.MIMEType, MIMETextPlain)))
	}

	if strings.TrimSpace(in.OCRText) == "" {
		issues = append(issues, NewIssue(CodeMissingOCRText, "",
			"ocrText is required: this slice extracts from supplied OCR text and never reads a scan"))
	}

	return issues
}

// Match records how a quote was found, so a reader can tell a verbatim hit from
// one that only matched after OCR normalization.
type Match string

const (
	// MatchExact is a byte-for-byte hit in the OCR text.
	MatchExact Match = "exact"
	// MatchFolded is a hit after case folding and accent stripping.
	MatchFolded Match = "folded"
	// MatchOCR is a hit only after folding confusable characters such as O/0.
	MatchOCR Match = "ocr_normalized"
)

// Evidence is the span of OCR text a field was read from.
//
// Start and End are rune offsets, not byte offsets, and End is exclusive. Quote
// holds the characters exactly as they appear in the OCR text, so a reader can
// find a field by searching the text for its quote. Coordinates are derived in
// Go by locating the quote; no model output can carry or influence them.
type Evidence struct {
	Quote string `json:"quote"`
	Page  int    `json:"page"`
	Line  int    `json:"line"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Match Match  `json:"match"`
}

// Runes returns the number of runes the evidence covers.
func (e Evidence) Runes() int { return e.End - e.Start }

// Field is one extracted value. Evidence is never empty: a value that cannot be
// pointed at in the OCR text is reported as an error instead of becoming a
// field, which is what makes the "no invented values" claim structural.
type Field struct {
	Name       string   `json:"name"`
	Value      string   `json:"value"`
	Confidence float64  `json:"confidence"`
	Evidence   Evidence `json:"evidence"`
}

// Severity distinguishes a problem that must block a record from one that only
// needs a human to look at it.
type Severity string

const (
	// SeverityError marks a finding that invalidates the extraction.
	SeverityError Severity = "error"
	// SeverityWarning marks a finding that must reach a reviewer.
	SeverityWarning Severity = "warning"
)

// Issue codes. Every finding carries one so that a caller can act on a category
// instead of matching on message text.
const (
	CodeMissingDocumentID    = "missing_document_id"
	CodeUnsupportedMIMEType  = "unsupported_mime_type"
	CodeMissingOCRText       = "missing_ocr_text"
	CodeMissingRequiredField = "missing_required_field"
	CodeUnknownField         = "unknown_field"
	CodeDuplicateField       = "duplicate_field"
	CodeEvidenceNotFound     = "evidence_not_found"
	CodeValueNotInEvidence   = "value_not_in_evidence"
	CodeInvalidConfidence    = "invalid_confidence"
	CodeMalformedAmount      = "malformed_amount"
	CodeMalformedDate        = "malformed_date"
	CodeArithmeticMismatch   = "arithmetic_mismatch"
	CodeRepeatedValue        = "repeated_value"
)

// Issue is one explicit error or warning, corresponding to the errors[] member
// of the contract. A model answer can never suppress one: issues are produced by
// the Go layers only.
type Issue struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Field    string   `json:"field,omitempty"`
	Message  string   `json:"message"`
}

// Error implements error so an issue can travel as one when a single finding is
// the whole failure.
func (i Issue) Error() string {
	if i.Field == "" {
		return i.Code + ": " + i.Message
	}
	return i.Code + " [" + i.Field + "]: " + i.Message
}

// NewIssue builds an error-severity issue.
func NewIssue(code, field, message string) Issue {
	return Issue{Code: code, Severity: SeverityError, Field: field, Message: message}
}

// NewWarning builds a warning-severity issue: something a reviewer must see that
// does not by itself invalidate the extraction.
func NewWarning(code, field, message string) Issue {
	return Issue{Code: code, Severity: SeverityWarning, Field: field, Message: message}
}

// SumRule is an arithmetic relationship the rules layer recomputes instead of
// trusting, so a total that does not add up is caught even when the model is
// confident about it.
type SumRule struct {
	Addends []string `json:"addends"`
	Total   string   `json:"total"`
}

// Policy decides what a document must yield and when a field needs a human.
type Policy struct {
	Version string `json:"policyVersion"`
	// Required names the fields every document must produce.
	Required []string `json:"requiredFields"`
	// ReviewBelow is the confidence at or under which a field must be reviewed.
	ReviewBelow float64 `json:"reviewBelow"`
	// Arithmetic names the sums that must hold.
	Arithmetic []SumRule `json:"arithmetic"`
}

// DefaultPolicy is the invoice policy used by the command and the fixtures: four
// required fields, a review threshold of 0.80, and subtotal + tax = total.
func DefaultPolicy() Policy {
	return Policy{
		Version: DefaultPolicyVersion,
		Required: []string{
			FieldDocumentNumber,
			FieldIssueDate,
			FieldCurrency,
			FieldTotal,
		},
		ReviewBelow: 0.80,
		Arithmetic: []SumRule{
			{Addends: []string{FieldSubtotal, FieldTax}, Total: FieldTotal},
		},
	}
}

// Validate reports the shape problems that would make a policy unusable. It
// exists because a malformed policy would otherwise silently disable a check.
func (p Policy) Validate() []Issue {
	var issues []Issue
	if strings.TrimSpace(p.Version) == "" {
		issues = append(issues, NewIssue(CodeMissingDocumentID, "", "policyVersion is required"))
	}
	if p.ReviewBelow < 0 || p.ReviewBelow > 1 {
		issues = append(issues, NewIssue(CodeInvalidConfidence, "",
			fmt.Sprintf("reviewBelow must be between 0 and 1, got %v", p.ReviewBelow)))
	}
	for _, rule := range p.Arithmetic {
		if len(rule.Addends) == 0 {
			issues = append(issues, NewIssue(CodeArithmeticMismatch, rule.Total,
				"an arithmetic rule needs at least one addend"))
		}
		if strings.TrimSpace(rule.Total) == "" {
			issues = append(issues, NewIssue(CodeArithmeticMismatch, "",
				"an arithmetic rule needs a total field"))
		}
	}
	return issues
}

// Review is the review-queue gate state. Required is true when any field fell
// below the policy confidence or when the run raised an error, and Reasons names
// why in a stable order so the same extraction always produces the same gate.
type Review struct {
	Required bool     `json:"required"`
	Reasons  []string `json:"reasons,omitempty"`
}

// Extraction is what comes back.
type Extraction struct {
	DocumentID string  `json:"documentId"`
	Policy     string  `json:"policyVersion"`
	Fields     []Field `json:"fields"`
	Errors     []Issue `json:"errors"`
	Review     Review  `json:"review"`
}

// Field returns the named field and whether it was present.
func (e Extraction) Field(name string) (Field, bool) {
	for _, f := range e.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// HasErrors reports whether any error-severity issue was raised.
func (e Extraction) HasErrors() bool {
	for _, issue := range e.Errors {
		if issue.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Validate checks the shape of an extraction before it is printed or stored. It
// is the self-check the command runs, so an extraction can never leave the
// program claiming a field with no real evidence or an impossible confidence.
func (e Extraction) Validate() []Issue {
	var issues []Issue

	if strings.TrimSpace(e.DocumentID) == "" {
		issues = append(issues, NewIssue(CodeMissingDocumentID, "", "documentId is required"))
	}

	seen := make(map[string]bool, len(e.Fields))
	for _, f := range e.Fields {
		if strings.TrimSpace(f.Name) == "" {
			issues = append(issues, NewIssue(CodeUnknownField, "", "a field has an empty name"))
			continue
		}
		if seen[f.Name] {
			issues = append(issues, NewIssue(CodeDuplicateField, f.Name,
				"the same field name appears more than once"))
		}
		seen[f.Name] = true

		if strings.TrimSpace(f.Value) == "" {
			issues = append(issues, NewIssue(CodeValueNotInEvidence, f.Name,
				"a field has an empty value"))
		}
		if f.Confidence < 0 || f.Confidence > 1 {
			issues = append(issues, NewIssue(CodeInvalidConfidence, f.Name,
				fmt.Sprintf("confidence must be between 0 and 1, got %v", f.Confidence)))
		}
		if strings.TrimSpace(f.Evidence.Quote) == "" {
			issues = append(issues, NewIssue(CodeEvidenceNotFound, f.Name,
				"a field carries no evidence quote"))
		} else if f.Evidence.Page < 0 || f.Evidence.Line < 0 || f.Evidence.Start < 0 || f.Evidence.End <= f.Evidence.Start {
			// Evidence is checked as a unit. Once the span is not a range, the
			// match kind is a consequence of the same fact, and reporting it
			// separately would bury the finding that can actually be acted on.
			issues = append(issues, NewIssue(CodeEvidenceNotFound, f.Name,
				fmt.Sprintf("evidence span is not a valid rune range: page=%d line=%d start=%d end=%d",
					f.Evidence.Page, f.Evidence.Line, f.Evidence.Start, f.Evidence.End)))
		} else {
			switch f.Evidence.Match {
			case MatchExact, MatchFolded, MatchOCR:
			default:
				issues = append(issues, NewIssue(CodeEvidenceNotFound, f.Name,
					fmt.Sprintf("evidence match %q is not one of %s, %s, %s",
						f.Evidence.Match, MatchExact, MatchFolded, MatchOCR)))
			}
		}
	}

	return issues
}

// CandidateField is the only shape a model may produce, and the shape a caller
// may hand to Evaluate from a frozen answer file.
//
// It deliberately has no evidence, coordinate or page member: every span is
// derived in Go by locating the quote in the OCR text, so no model output can
// invent, move or widen the evidence a field points at.
type CandidateField struct {
	Name       string  `json:"name"`
	Value      string  `json:"value"`
	Quote      string  `json:"quote"`
	Confidence float64 `json:"confidence"`
}

// CandidateAnswer is a model's whole answer. Agent.Extract returns one and
// Evaluate is the only thing that turns it into fields.
type CandidateAnswer struct {
	Fields []CandidateField `json:"fields"`
}
