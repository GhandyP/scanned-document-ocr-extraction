package extraction

import "strings"

// Locate finds quote in ocrText, preferring a byte-exact match, then a
// case/accent-folded match, then a match normalized for common OCR confusables.
// The returned quote always comes from the original OCR text.
func Locate(ocrText, quote string) (Evidence, bool) {
	if strings.TrimSpace(quote) == "" {
		return Evidence{}, false
	}

	byteOffsets := runeByteOffsets(ocrText)
	textRunes := []rune(ocrText)
	quoteRuneCount := len([]rune(quote))

	// Exact matching compares the original byte sequence at rune boundaries.
	for start := 0; start+quoteRuneCount < len(byteOffsets); start++ {
		end := start + quoteRuneCount
		if ocrText[byteOffsets[start]:byteOffsets[end]] == quote {
			return evidenceAt(ocrText, byteOffsets, textRunes, start, end, MatchExact), true
		}
	}

	foldedText := []rune(Fold(ocrText))
	foldedQuote := []rune(Fold(quote))
	if start, ok := findRunes(foldedText, foldedQuote); ok {
		return evidenceAt(ocrText, byteOffsets, textRunes, start, start+len(foldedQuote), MatchFolded), true
	}

	ocrTextRunes := []rune(foldOCR(ocrText))
	ocrQuoteRunes := []rune(foldOCR(quote))
	if start, ok := findRunes(ocrTextRunes, ocrQuoteRunes); ok {
		return evidenceAt(ocrText, byteOffsets, textRunes, start, start+len(ocrQuoteRunes), MatchOCR), true
	}

	return Evidence{}, false
}

// Fold lowercases and strips common Latin accents while preserving rune count.
// If lowercasing a rune ever yields anything other than one rune, it is kept
// unchanged so folded offsets remain usable as offsets into the original text.
func Fold(s string) string {
	var folded strings.Builder
	for _, r := range s {
		lowered := []rune(strings.ToLower(string(r)))
		if len(lowered) != 1 {
			folded.WriteRune(r)
			continue
		}
		folded.WriteRune(stripAccent(lowered[0]))
	}
	return folded.String()
}

func stripAccent(r rune) rune {
	switch r {
	case 'à', 'á', 'â', 'ã', 'ä', 'å':
		return 'a'
	case 'è', 'é', 'ê', 'ë':
		return 'e'
	case 'ì', 'í', 'î', 'ï':
		return 'i'
	case 'ò', 'ó', 'ô', 'õ', 'ö':
		return 'o'
	case 'ù', 'ú', 'û', 'ü':
		return 'u'
	case 'ñ':
		return 'n'
	case 'ç':
		return 'c'
	case 'ý', 'ÿ':
		return 'y'
	default:
		return r
	}
}

// foldOCR applies one-rune OCR confusables to the length-preserving fold.
// Multi-rune confusions such as m/rn or w/vv are deliberately unsupported:
// they would change rune length and invalidate evidence offsets.
func foldOCR(s string) string {
	runes := []rune(Fold(s))
	for i, r := range runes {
		switch r {
		case '0', 'o':
			runes[i] = '0'
		case '1', 'l', 'i':
			runes[i] = '1'
		case '5', 's':
			runes[i] = '5'
		case '8', 'b':
			runes[i] = '8'
		case '2', 'z':
			runes[i] = '2'
		case '6', 'g':
			runes[i] = '6'
		}
	}
	return string(runes)
}

func findRunes(text, quote []rune) (int, bool) {
	if len(quote) == 0 || len(quote) > len(text) {
		return 0, false
	}
	for start := 0; start+len(quote) <= len(text); start++ {
		match := true
		for i := range quote {
			if text[start+i] != quote[i] {
				match = false
				break
			}
		}
		if match {
			return start, true
		}
	}
	return 0, false
}

func runeByteOffsets(s string) []int {
	offsets := make([]int, 0, len([]rune(s))+1)
	for offset := range s {
		offsets = append(offsets, offset)
	}
	return append(offsets, len(s))
}

func evidenceAt(ocrText string, byteOffsets []int, textRunes []rune, start, end int, match Match) Evidence {
	page, line := 1, 1
	for _, r := range textRunes[:start] {
		switch r {
		case '\f':
			page++
			line = 1
		case '\n':
			line++
		}
	}

	return Evidence{
		Quote: ocrText[byteOffsets[start]:byteOffsets[end]],
		Page:  page,
		Line:  line,
		Start: start,
		End:   end,
		Match: match,
	}
}
