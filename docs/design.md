# Design

## The problem this shape solves

A model reading OCR text can produce a value the text does not contain, and a
downstream record cannot tell that from a value the text does contain. The
defence in this repository is not a better prompt. It is that the model's answer
passes through a fixed pipeline in Go, and the pipeline decides what a field is.

## The pipeline

`Evaluate` is the only place a candidate becomes a field. It runs the stages in a
fixed order and appends to one issue list, so a caller sees the whole picture
rather than the first problem.

1. **Policy validation.** A policy that would silently disable a check is itself
   an issue. A malformed sum rule, an impossible threshold or a missing version
   fails loudly instead of switching a check off.
2. **Input validation, which blocks.** A `DocumentInput` the contract rejects —
   no document ID, no OCR text, a media type this slice cannot read — yields no
   fields at all. A field that cannot be keyed to a document cannot enter a
   review queue either, so the input is refused as a whole and the caller sees
   exactly which parts are missing.
3. **Per candidate checks**, in candidate order:
   - an empty name is `unknown_field`, a repeated name is `duplicate_field`;
   - a confidence outside `[0,1]` is `invalid_confidence`;
   - a quote `Locate` cannot find is `evidence_not_found`, and the candidate is
     dropped rather than repaired;
   - the evidence text and the model's own value are each put through the same
     normaliser, and a disagreement is `value_not_in_evidence`.
4. **Repeated labels.** An amount label the text names more than once is
   `repeated_value`, because the value comparison cannot tell which reading the
   document means.
5. **Required fields.** Anything the policy requires that the document did not
   yield is `missing_required_field`.
6. **Arithmetic.** Every sum the policy names is recomputed in hundredths. A rule
   is only checked when every operand it needs was actually extracted, because
   two missing operands are not evidence that a sum is wrong.
7. **The review gate.** Any issue of any severity, or any field at or below the
   threshold, requires review.

## Where each claim is held

Each row is a claim the README makes, the code that enforces it, and the test
that would fail if the claim stopped being true. Nothing in the README is claimed
without a row here.

| Claim | Enforced in | Held by |
| --- | --- | --- |
| The model's answer shape has no place for evidence | `CandidateField`, `OutputSchema` | `TestCandidateFieldCarriesNoEvidence`, `TestOutputSchemaIsCandidateAnswerOnly` |
| A field always carries a real span | `Extraction.Validate` | `TestExtractionValidateRejectsFieldsWithoutRealEvidence` |
| A value whose quote is absent never becomes a field | `evaluateCandidates` | `TestEvaluateAgainstDocumentedFixtures` (case `inv-010`) |
| The reported value comes from the evidence, not the model | `evaluateCandidates` | `TestEvaluateAgainstDocumentedFixtures` (case `inv-009`) |
| Offsets are rune offsets after accents and non-BMP text | `Locate`, `runeByteOffsets` | `TestLocateRuneOffsetsAfterAccentsAndEmoji` |
| The fold preserves rune count | `Fold` | `TestFoldLengthPreservingAndAccents` |
| A verbatim hit beats a normalised one | `Locate` | `TestLocatePrefersAVerbatimHitToANormalizedOne` |
| The quote keeps the document's own characters | `Locate` | `TestLocateConfusableFallback`, `TestLocateAccentFolding` |
| Page and line are derived, never supplied | `Locate`, `evidenceAt` | `TestLocatePageAndLineAcrossFormFeedAndNewlines`, `TestEvaluateAgainstDocumentedFixtures` (case `inv-007`) |
| Both separator conventions canonicalise to one form | `normalizeAmount` | `TestNormalizeAmountReadsBothSeparatorConventions` |
| An amount that cannot be read with certainty is refused | `normalizeAmount` | `TestNormalizeAmountRejectsWhatItCannotRead` |
| A date that is not on the calendar is refused | `normalizeDate` | `TestNormalizeDateReadsTheDocumentedFormats` |
| Sums are recomputed, not believed | `checkArithmetic` | `TestEvaluateAgainstDocumentedFixtures` (case `inv-006`) |
| A repeated label is surfaced | `repeatedLabels` | `TestEvaluateFlagsARepeatedAmountLabelFromTheTextAlone` |
| An incomplete input is not half-extracted | `Evaluate` | `TestEvaluateTreatsAnIncompleteInputAsUnreadable` |
| The agent has no tools | `NewAgent` | `TestNewAgentConfigHasNoTools` |
| One session per document | `SessionIDFor` | `TestSessionIDForIsDeterministicAndDocumentScoped` |
| A malformed answer is an error, never an empty extraction | `parseCandidateAnswer` | `TestAgentExtractRejectsMalformedOrMissingFinalResponse` |
| The command turns the gate into an exit code | `report`, `main` | `TestBinaryExitCodes` |

The fixture test additionally re-checks the central promise from outside the
implementation: for every reported field it asserts that the quote really occurs
in the OCR text and that the reported span selects exactly that quote. A field
that survived the pipeline without that property fails there even if the pipeline
believes it succeeded.

## The OCR fold

Matching tries three levels in order and records which one hit in
`Evidence.Match`:

1. `exact` — a byte-for-byte hit.
2. `folded` — a hit after lower-casing and stripping Latin accents.
3. `ocr_normalized` — a hit after also mapping the one-rune confusions
   `{0,O,o}`, `{1,l,I}`, `{5,S,s}`, `{8,B}`, `{2,Z}`, `{6,G}`.

Every level maps one rune to exactly one rune. That is the property that makes
the design work: an index in the folded text is already an index in the original,
so the span can be reported against the document rather than against a
transformed copy of it. Multi-rune confusions such as `m`/`rn` are deliberately
not handled, because they would change the rune length and break that property.

A consequence worth stating: folding makes matching more permissive, so the
lowest-offset hit at the strongest level wins, and a normalized hit is labelled
as one. A reader can always tell a quotation from a reconstruction.

## The review gate

The gate is the only thing standing between a readable extraction and a stored
record, and this slice deliberately has no destination adapter, so the gate is
reported rather than enforced by a write. It is expressed as exit code `3`, and
`Review.Reasons` names every reason in a stable order, so the same extraction
always produces the same gate and the same explanation.

## Known limits

Stated in the README and repeated here because they are design decisions rather
than defects:

- Dates are read day first. A slash or dot separated date is ambiguous, so the
  convention is declared once instead of guessed per document.
- An amount with more than two fraction digits is refused rather than rounded.
- The input and the answer are parsed strictly. An unknown member is an error,
  because it usually means the caller expects something this command does not do.
- Image and PDF inputs, an OCR engine, page coordinate mapping, destination
  adapters and the field-validator sub-agent are out of this slice.
