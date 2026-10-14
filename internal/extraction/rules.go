package extraction

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// amountFieldLabels names the labels a document may use for each amount field.
// The list exists because the field vocabulary is English while a real document
// is not: a Spanish invoice labels the tax "IVA" or "Impuesto" and the model is
// free to map either onto the tax field.
var amountFieldLabels = map[string][]string{
	FieldSubtotal: {"subtotal", "sub total", "base", "neto", "net"},
	FieldTax:      {"tax", "iva", "impuesto", "vat", "igv"},
	FieldTotal:    {"total", "importe total", "total a pagar"},
}

// Evaluate turns a model answer into an extraction a caller can audit.
//
// It is the only place a candidate becomes a field, and it is deliberately
// unforgiving. A candidate whose quote cannot be located in the OCR text is
// reported as an error instead of becoming a field, and a candidate whose own
// stated value disagrees with the text it quoted is rejected rather than
// repaired. The value that is reported is always derived from the evidence, so
// a field can never carry a number the document does not state.
//
// The four stages run in a fixed order: input problems, per candidate checks,
// repeated labels, then the policy. Every stage appends to the same issue list,
// so a caller sees the whole picture instead of the first problem.
func Evaluate(in DocumentInput, answer CandidateAnswer, policy Policy) Extraction {
	out := Extraction{
		DocumentID: strings.TrimSpace(in.DocumentID),
		Policy:     policy.Version,
		Fields:     []Field{},
		Errors:     []Issue{},
	}

	out.Errors = append(out.Errors, policy.Validate()...)

	// An input the contract requires but the caller left incomplete yields no
	// fields at all, not a half answer with a warning attached. A field that
	// cannot be keyed to a document cannot enter a review queue either, so the
	// input is refused as a whole and the caller sees exactly which parts are
	// missing.
	if inputIssues := in.Validate(); len(inputIssues) != 0 {
		out.Errors = append(out.Errors, inputIssues...)
		out.Review = reviewFor(out.Fields, out.Errors, policy)
		return out
	}

	out.Fields = append(out.Fields, evaluateCandidates(in, answer, &out.Errors)...)

	out.Errors = append(out.Errors, repeatedLabels(in.OCRText, out.Fields)...)
	out.Errors = append(out.Errors, requiredFields(out, policy)...)
	out.Errors = append(out.Errors, checkArithmetic(out, policy)...)
	out.Review = reviewFor(out.Fields, out.Errors, policy)

	return out
}

// evaluateCandidates applies the per candidate checks. A rejected candidate
// never becomes a field; the reason is appended to issues.
func evaluateCandidates(in DocumentInput, answer CandidateAnswer, issues *[]Issue) []Field {
	fields := make([]Field, 0, len(answer.Fields))
	seen := make(map[string]bool, len(answer.Fields))

	for _, candidate := range answer.Fields {
		name := strings.TrimSpace(candidate.Name)

		if name == "" {
			*issues = append(*issues, NewIssue(CodeUnknownField, "",
				"a candidate field carries no name"))
			continue
		}
		if seen[name] {
			*issues = append(*issues, NewIssue(CodeDuplicateField, name,
				"the model returned this field more than once, so the document is ambiguous"))
			continue
		}
		seen[name] = true

		if candidate.Confidence < 0 || candidate.Confidence > 1 {
			*issues = append(*issues, NewIssue(CodeInvalidConfidence, name,
				fmt.Sprintf("confidence %v is outside the range 0 to 1", candidate.Confidence)))
			continue
		}

		evidence, found := Locate(in.OCRText, candidate.Quote)
		if !found {
			*issues = append(*issues, NewIssue(CodeEvidenceNotFound, name,
				fmt.Sprintf("the quoted evidence %q does not appear anywhere in the OCR text", candidate.Quote)))
			continue
		}

		derived, readable := normalizeField(name, evidence.Quote)
		if !readable {
			*issues = append(*issues, NewIssue(codeForField(name), name,
				fmt.Sprintf("the evidence %q does not contain a readable %s", evidence.Quote, description(name))))
			continue
		}

		stated, readable := normalizeField(name, candidate.Value)
		if !readable || stated != derived {
			*issues = append(*issues, NewIssue(CodeValueNotInEvidence, name,
				fmt.Sprintf("the value %q disagrees with the evidence %q, which states %s",
					candidate.Value, evidence.Quote, derived)))
			continue
		}

		fields = append(fields, Field{
			Name:       name,
			Value:      derived,
			Confidence: candidate.Confidence,
			Evidence:   evidence,
		})
	}

	return fields
}

func description(name string) string {
	switch name {
	case FieldSubtotal, FieldTax, FieldTotal:
		return "amount"
	case FieldIssueDate:
		return "date"
	case FieldCurrency:
		return "currency code"
	default:
		return "value"
	}
}

func codeForField(name string) string {
	switch name {
	case FieldSubtotal, FieldTax, FieldTotal:
		return CodeMalformedAmount
	case FieldIssueDate:
		return CodeMalformedDate
	default:
		return CodeValueNotInEvidence
	}
}

// repeatedLabels warns when the OCR text names the same amount label more than
// once, which is the S8 "repeated totals" case. The value comparison cannot
// catch it: both readings can be internally consistent, and only the reader can
// say which one the document means.
//
// Counting happens on the OCR-folded text so that "5ubt0ta1" and "Subtotal" are
// the same label, and it requires a whole-word match so that the "total" inside
// "subtotal" is not mistaken for a second total.
func repeatedLabels(ocrText string, fields []Field) []Issue {
	folded := []rune(foldOCR(ocrText))
	var issues []Issue

	for _, field := range fields {
		labels, ok := amountFieldLabels[field.Name]
		if !ok {
			continue
		}
		most := 0
		for _, label := range labels {
			if count := countWholeWord(folded, []rune(foldOCR(label))); count > most {
				most = count
			}
		}
		if most > 1 {
			issues = append(issues, NewWarning(CodeRepeatedValue, field.Name,
				fmt.Sprintf("a label for %s appears %d times in the OCR text, so the value it names is ambiguous",
					field.Name, most)))
		}
	}

	return issues
}

func countWholeWord(text, word []rune) int {
	if len(word) == 0 || len(word) > len(text) {
		return 0
	}
	count := 0
	for start := 0; start+len(word) <= len(text); start++ {
		matched := true
		for i := range word {
			if text[start+i] != word[i] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if start > 0 && isWordRune(text[start-1]) {
			continue
		}
		if end := start + len(word); end < len(text) && isWordRune(text[end]) {
			continue
		}
		count++
	}
	return count
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func requiredFields(out Extraction, policy Policy) []Issue {
	var issues []Issue
	for _, name := range policy.Required {
		if _, ok := out.Field(name); !ok {
			issues = append(issues, NewIssue(CodeMissingRequiredField, name,
				fmt.Sprintf("the document did not yield the required field %s", name)))
		}
	}
	return issues
}

// checkArithmetic recomputes every sum the policy names rather than trusting the
// model. A rule is only checked when every operand it needs was actually
// extracted: two missing operands are not evidence that a sum is wrong.
func checkArithmetic(out Extraction, policy Policy) []Issue {
	var issues []Issue

	for _, rule := range policy.Arithmetic {
		total, ok := out.Field(rule.Total)
		if !ok {
			continue
		}
		want, ok := minorUnits(total.Value)
		if !ok {
			continue
		}

		sum := int64(0)
		operands := make([]string, 0, len(rule.Addends))
		complete := true
		for _, name := range rule.Addends {
			field, ok := out.Field(name)
			if !ok {
				complete = false
				break
			}
			value, ok := minorUnits(field.Value)
			if !ok {
				complete = false
				break
			}
			sum += value
			operands = append(operands, fmt.Sprintf("%s = %s", name, field.Value))
		}
		if !complete {
			continue
		}

		if sum != want {
			issues = append(issues, NewIssue(CodeArithmeticMismatch, rule.Total,
				fmt.Sprintf("%s is %s but %s adds up to %s",
					rule.Total, total.Value, strings.Join(operands, " + "), formatMinorUnits(sum))))
		}
	}

	return issues
}

// reviewFor fails closed. Anything a human should look at puts the extraction in
// the review queue: an explicit issue of any severity, or a field whose
// confidence did not clear the policy threshold.
func reviewFor(fields []Field, issues []Issue, policy Policy) Review {
	review := Review{}

	for _, issue := range issues {
		review.Required = true
		review.Reasons = append(review.Reasons, fmt.Sprintf("%s: %s", issue.Code, issue.Message))
	}

	for _, field := range fields {
		if field.Confidence <= policy.ReviewBelow {
			review.Required = true
			review.Reasons = append(review.Reasons,
				fmt.Sprintf("field %s has confidence %.2f, at or below the policy threshold %.2f",
					field.Name, field.Confidence, policy.ReviewBelow))
		}
	}

	return review
}

// extractToken returns the part of a line after its last colon, which is where a
// document puts the value a label names. It is what keeps a digit inside the
// label itself, such as the 21 in "IVA (21%): 260.40", out of the amount.
func extractToken(text string) string {
	if idx := strings.LastIndex(text, ":"); idx >= 0 {
		return strings.TrimSpace(text[idx+1:])
	}
	return strings.TrimSpace(text)
}

// normalizeField returns the canonical form of a field's value, derived from the
// text that carries it. Both the model's value and the evidence quote are put
// through this same function, so the two are only compared after they mean the
// same thing.
func normalizeField(name, text string) (string, bool) {
	switch name {
	case FieldDocumentNumber:
		return nonEmpty(extractToken(text))
	case FieldCurrency:
		code := strings.ToUpper(extractToken(text))
		if len(code) == 3 && allLetters(code) {
			return code, true
		}
		return "", false
	case FieldIssueDate:
		return normalizeDate(text)
	case FieldSubtotal, FieldTax, FieldTotal:
		return normalizeAmount(text)
	default:
		return nonEmpty(extractToken(text))
	}
}

func nonEmpty(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	return s, true
}

var datePattern = regexp.MustCompile(`\d{1,4}[./-]\d{1,2}[./-]\d{1,4}`)

// dateLayouts are tried in order. A four digit leading group is read as ISO, and
// anything else is read day first, which is the convention this slice accepts
// and states in its README. A slash separated date such as 03/04/2026 is
// genuinely ambiguous, so the convention is a documented limitation rather than
// a guess made per document.
var dateLayouts = []string{
	"2006-01-02",
	"02-01-2006", "2-1-2006",
	"02/01/2006", "2/1/2006",
	"02.01.2006", "2.1.2006",
}

// normalizeDate returns an ISO calendar date, and refuses anything that is not a
// real day on the calendar.
func normalizeDate(text string) (string, bool) {
	match := datePattern.FindString(extractToken(text))
	if match == "" {
		return "", false
	}
	for _, layout := range dateLayouts {
		parsed, err := time.Parse(layout, match)
		if err == nil {
			return parsed.Format("2006-01-02"), true
		}
	}
	return "", false
}

// normalizeAmount returns an amount in a single canonical shape: an optional
// minus sign, digits, and exactly two fraction digits, with a dot as the only
// separator. The evidence quote keeps the document's own formatting, so both the
// printed form and the canonical form stay available to a reviewer.
//
// It refuses anything it cannot read with certainty rather than guessing, which
// is why "1,2,3" is rejected instead of being read as 123.
func normalizeAmount(text string) (string, bool) {
	token := extractToken(text)

	start := -1
	for i := 0; i < len(token); i++ {
		if isASCIIDigit(token[i]) || token[i] == '+' || token[i] == '-' {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}

	end := start
	if token[end] == '+' || token[end] == '-' {
		end++
	}
	for end < len(token) && (isASCIIDigit(token[end]) || token[end] == '.' || token[end] == ',') {
		end++
	}

	core := token[start:end]
	// A digit left over after the number means this text is not a plain amount.
	// It is what stops a document number such as F-2026-0431 from being read as
	// the number 2026.
	for i := end; i < len(token); i++ {
		if isASCIIDigit(token[i]) {
			return "", false
		}
	}

	return canonicalAmount(core)
}

func canonicalAmount(core string) (string, bool) {
	if core == "" {
		return "", false
	}

	sign := ""
	if core[0] == '+' || core[0] == '-' {
		if core[0] == '-' {
			sign = "-"
		}
		core = core[1:]
	}
	if core == "" || strings.HasSuffix(core, ".") || strings.HasSuffix(core, ",") {
		return "", false
	}

	lastDot := strings.LastIndex(core, ".")
	lastComma := strings.LastIndex(core, ",")

	var whole, fraction string

	switch {
	case lastDot >= 0 && lastComma >= 0:
		// Both separators are present: the rightmost one is the decimal mark and
		// the other groups thousands.
		decimal, thousands := lastDot, ","
		if lastComma > lastDot {
			decimal, thousands = lastComma, "."
		}
		whole, fraction = core[:decimal], core[decimal+1:]
		if !groupedDigits(whole, thousands) {
			return "", false
		}
		whole = strings.ReplaceAll(whole, thousands, "")

	case lastDot >= 0 || lastComma >= 0:
		separator := "."
		if lastComma >= 0 {
			separator = ","
		}
		index := strings.LastIndex(core, separator)
		after := len(core) - index - 1

		switch {
		case strings.Count(core, separator) > 1:
			if !groupedDigits(core, separator) {
				return "", false
			}
			whole = strings.ReplaceAll(core, separator, "")
		case after == 1 || after == 2:
			whole, fraction = core[:index], core[index+1:]
		case after == 3:
			if !groupedDigits(core, separator) {
				return "", false
			}
			whole = strings.ReplaceAll(core, separator, "")
		default:
			return "", false
		}

	default:
		whole = core
	}

	if !allDigits(whole) {
		return "", false
	}
	if whole == "" || len(whole) > 18 {
		return "", false
	}

	switch len(fraction) {
	case 0:
		fraction = "00"
	case 1:
		fraction += "0"
	case 2:
	default:
		// More precision than this slice claims to represent, so it is refused
		// rather than rounded.
		return "", false
	}
	if !allDigits(fraction) {
		return "", false
	}

	trimmed := strings.TrimLeft(whole, "0")
	if trimmed == "" {
		trimmed = "0"
	}
	if trimmed == "0" {
		sign = ""
	}

	return sign + trimmed + "." + fraction, true
}

// groupedDigits checks a group of thousands separated digits, where every group
// after the first must be exactly three digits.
func groupedDigits(s, separator string) bool {
	groups := strings.Split(s, separator)
	if len(groups) == 0 {
		return false
	}
	if len(groups[0]) < 1 || len(groups[0]) > 3 || !allDigits(groups[0]) {
		return false
	}
	for _, group := range groups[1:] {
		if len(group) != 3 || !allDigits(group) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isASCIIDigit(s[i]) {
			return false
		}
	}
	return true
}

func allLetters(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return s != ""
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// minorUnits converts a canonical amount into hundredths, which is the scale the
// arithmetic check compares on.
func minorUnits(canonical string) (int64, bool) {
	sign := int64(1)
	value := canonical
	if strings.HasPrefix(value, "-") {
		sign = -1
		value = value[1:]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[1]) != 2 {
		return 0, false
	}
	parsed, err := strconv.ParseInt(parts[0]+parts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return sign * parsed, true
}

func formatMinorUnits(value int64) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	return fmt.Sprintf("%s%d.%02d", sign, value/100, value%100)
}
