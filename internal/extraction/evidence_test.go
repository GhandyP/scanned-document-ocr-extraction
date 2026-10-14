package extraction

import "testing"

func TestLocateRuneOffsetsAfterAccentsAndEmoji(t *testing.T) {
	const ocrText = "é😊 Sesión"

	got, ok := Locate(ocrText, "Sesion")
	if !ok {
		t.Fatal("Locate() did not find the folded quote")
	}
	if got.Start != 3 || got.End != 9 {
		t.Errorf("rune span = [%d:%d], want [3:9]", got.Start, got.End)
	}
	if got.Quote != "Sesión" {
		t.Errorf("Quote = %q, want original OCR text %q", got.Quote, "Sesión")
	}
	if got.Match != MatchFolded {
		t.Errorf("Match = %q, want %q", got.Match, MatchFolded)
	}
	if span := string([]rune(ocrText)[got.Start:got.End]); span != got.Quote {
		t.Errorf("rune span = %q, Quote = %q", span, got.Quote)
	}
}

func TestLocateConfusableFallback(t *testing.T) {
	got, ok := Locate("T0ta1", "Total")
	if !ok {
		t.Fatal("Locate() did not find OCR-confusable quote")
	}
	if got.Match != MatchOCR {
		t.Errorf("Match = %q, want %q", got.Match, MatchOCR)
	}
	if got.Quote != "T0ta1" {
		t.Errorf("Quote = %q, want original OCR text %q", got.Quote, "T0ta1")
	}
}

func TestLocateAccentFolding(t *testing.T) {
	got, ok := Locate("El número: Sesión.", "Sesion")
	if !ok {
		t.Fatal("Locate() did not find accent-folded quote")
	}
	if got.Match != MatchFolded {
		t.Errorf("Match = %q, want %q", got.Match, MatchFolded)
	}
	if got.Quote != "Sesión" {
		t.Errorf("Quote = %q, want original accented text %q", got.Quote, "Sesión")
	}
}

// TestLocatePrefersAVerbatimHitToANormalizedOne pins the precedence the three
// level design promises: a verbatim hit is a stronger claim than a normalized
// one, so it wins even when the normalized hit appears earlier in the text.
//
// The first case is the one that has teeth. In "total x Total" a folded hit
// exists at offset 0, so an implementation that tried the normalized level
// first would answer with the wrong offset AND the wrong Match, and the second
// case alone would never notice.
func TestLocatePrefersAVerbatimHitToANormalizedOne(t *testing.T) {
	t.Run("a verbatim hit later in the text beats a folded hit earlier", func(t *testing.T) {
		got, ok := Locate("total x Total", "Total")
		if !ok {
			t.Fatal("Locate() did not find the quote")
		}
		if got.Start != 8 || got.End != 13 || got.Match != MatchExact {
			t.Errorf("Evidence = %+v, want the verbatim hit at [8:13] with match %q", got, MatchExact)
		}
		if got.Quote != "Total" {
			t.Errorf("Quote = %q, want %q", got.Quote, "Total")
		}
	})

	t.Run("a verbatim hit beats a confusable hit later", func(t *testing.T) {
		got, ok := Locate("Total luego T0tal", "Total")
		if !ok {
			t.Fatal("Locate() did not find the exact quote")
		}
		if got.Start != 0 || got.Match != MatchExact || got.Quote != "Total" {
			t.Errorf("Evidence = %+v, want the first exact occurrence", got)
		}
	})
}

func TestLocatePageAndLineAcrossFormFeedAndNewlines(t *testing.T) {
	got, ok := Locate("first\nline\fsecond\nTotal", "Total")
	if !ok {
		t.Fatal("Locate() did not find quote after form feed and newlines")
	}
	if got.Page != 2 || got.Line != 2 {
		t.Errorf("Page/Line = %d/%d, want 2/2", got.Page, got.Line)
	}
}

func TestLocateAbsentAndEmptyQuotes(t *testing.T) {
	for _, quote := range []string{"missing", "", " \n\t"} {
		t.Run(quote, func(t *testing.T) {
			if got, ok := Locate("some OCR text", quote); ok {
				t.Errorf("Locate() = (%+v, true), want not found", got)
			}
		})
	}
}

func TestFoldLengthPreservingAndAccents(t *testing.T) {
	const input = "ABC áàâãäå éèêë íìîï óòôõö úùûü ñ ç ýÿ ÁÀÂÃÄÅ 😊 ß"
	const want = "abc aaaaaa eeee iiii ooooo uuuu n c yy aaaaaa 😊 ß"

	got := Fold(input)
	if got != want {
		t.Errorf("Fold(%q) = %q, want %q", input, got, want)
	}
	if gotRunes, wantRunes := len([]rune(got)), len([]rune(input)); gotRunes != wantRunes {
		t.Errorf("Fold changed rune length from %d to %d", wantRunes, gotRunes)
	}
}
