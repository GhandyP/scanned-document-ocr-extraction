# Case brief

## The problem

Something upstream produced OCR text from a scanned document. That text is
lossy: characters are substituted, columns collapse into one stream, and a
number can be off by one glyph from what the page actually said.

An extraction step has to turn that text into named fields. The temptation is to
hand the text to a model and print its answer. That fails in three specific ways.

1. **The model supplies a value the text does not contain.** An invoice total of
   `1,240.00` is reported for a document whose text never states it. The answer
   is fluent and wrong, and nothing in the output makes that visible.
2. **A value is normalised into something the document never claimed.** A
   separator convention is guessed, or an accented character is replaced, and the
   stored value drifts from the printed one.
3. **A low-confidence field is treated as settled.** The extraction is written to
   an accounting or legal record before a human ever looks at the field that was
   guessed.

## What this repository does about it

**Evidence is structural, not a convention.** The schema the model may answer
with has no member for a span, a page or a line. It may return a name, a value,
a verbatim quote and a confidence, and nothing else. Every coordinate is derived
in Go by locating that quote in the OCR text. A model therefore has no field in
which to place an invented reference, and a value whose quote cannot be found in
the text is reported as an error instead of becoming a field.

**OCR noise is handled in the locator, not by rewriting the document.** Matching
folds case, strips accents and optionally folds classic OCR confusions such as
`O`/`0` and `l`/`1`. The fold is length preserving in runes, and the reported
quote always holds the original characters, so the evidence a reviewer reads is
the text the document actually contained. The confidence of the match is
reported too: a verbatim hit and a normalized hit are not the same claim.

**The arithmetic is recomputed, not believed.** A policy names the sums that must
hold, such as subtotal plus tax equals total, and the rules layer recomputes them
in pure Go. A model that is confident about a total that does not add up is
caught anyway.

**The gate fails closed.** Below the policy confidence, or in the presence of any
error, the extraction is marked for review. A record is never reported as ready
to store while a field is below policy.

## Scope of this slice

In scope: the strict extraction contract, deterministic validation in pure Go,
the schema-constrained ADK agent, and the review gate.

Out of scope, and named as such in the rejection messages rather than silently
unsupported: image and PDF ingestion, an OCR engine, page coordinate mapping, and
destination repository adapters. This slice consumes OCR text; it does not
produce it.
