# scanned-document-ocr-extraction

Extract typed fields from OCR text that something else produced, keep every
value attached to the exact characters it came from, and refuse to report a
record as ready while a field is below policy confidence.

Built with **Go 1.26** and **Google ADK Go v2** (Gemini).

This is a text-only extraction contract. It does **not** read a scan, an image or
a PDF, it does **not** run OCR, and it does **not** write to an accounting or
legal system. It turns supplied OCR text into typed fields a human can audit,
and it says when a human has to look.

## Why

Reading an invoice or a form out of noisy OCR text is a place where a confident
wrong answer is expensive. Three failure modes dominate:

1. A model returns a plausible total that appears nowhere in the text.
2. A value is quietly normalised into something the document never stated.
3. A low-confidence field is treated as settled and reaches a stored record.

This repository is built so all three are structurally hard.

## Design decisions

**The model has no member in which to place evidence.** The answer shape the
agent is asked for is `{name, value, quote, confidence}` and nothing else. There
is no `evidence`, no `page`, no `line`, no `start` and no `end` in the output
schema, so a model cannot invent, move or widen a span. Every coordinate is
derived in Go by locating the quote in the OCR text.

**A value whose quote cannot be found is an error, not a field.** If the quote
the model offers does not appear in the supplied text, the candidate is dropped
with `evidence_not_found`. It is never repaired into a field from the text that
happens to be nearby.

**The reported value is derived from the evidence.** Both the model's stated
value and the text it quoted are put through the same normaliser and then
compared. If they disagree, the field is rejected with `value_not_in_evidence`.
A model that says `999.00` while quoting `Total: 48.40` does not get to keep
`999.00`, and it does not get a field either.

**The evidence quote keeps the document's own characters; only the value is
canonicalised.** Amounts are reported as `1240.00` whether the page printed
`1,240.00` or `1.240,00`, but the quote still says `1.240,00`. A reviewer can
always see what the document actually contained next to what the program
understood.

**Offsets are rune offsets, not byte offsets.** Matching folds case and accents
through a length preserving mapping, so an index in the folded text is already an
index in the original. A quote that follows an accented character or an emoji
still reports the right span, and `Evidence.Quote` is always the original text:
the span for `Sesión` says `Sesión`, not `Sesion`.

**A verbatim hit beats a normalised one.** Matching tries a byte-exact hit
first, then a case and accent fold, then a fold that also maps one-rune OCR
confusions such as `O`/`0` and `l`/`1`. Each level records how it matched in
`Evidence.Match`, so a reader can tell a quotation from a reconstruction. Multi
rune confusions such as `m`/`rn` are deliberately not handled, because they would
change the rune length and break the offsets.

**The arithmetic is recomputed, not believed.** The policy names the sums that
must hold, and they are checked in hundredths in pure Go. A model that is
confident about `500.00` when `300.00 + 63.00` does not add up is caught anyway.

**A repeated label is surfaced rather than silently resolved.** When the OCR text
names the same amount label twice, as in a total followed by a previous total,
the run raises `repeated_value`. The value comparison cannot catch this: both
readings can be internally consistent, and only a reader can say which one the
document means.

**The gate fails closed.** Any issue of any severity, or any field at or below
the policy threshold, marks the extraction for review. The command then exits
`3` instead of `0`, so a caller cannot mistake "these fields are readable" for
"these fields are confirmed".

**The agent has no tools.** Extraction runs with an empty tool list, so it has no
mechanism to write, notify, route or fetch. A test asserts the configured agent
carries no tools.

**A response that is not exactly the contract is an error.** No final event, no
JSON, a missing member, an unknown member or trailing data all fail the run. The
answer is never defaulted, repaired, or read as an empty extraction.

## The contract

Input, and this is exactly `testdata/example-input.json`:

```json
{
  "documentId": "inv-001",
  "mimeType": "text/plain",
  "ocrText": "FACTURA\nNumero: F-1\nFecha: 15/03/2026\nMoneda: USD\nSubtotal: 10.00\nIVA: 2.10\nTotal: 12.10\n"
}
```

Output, abridged to the last field. Every number below is copied from a real
run; the command reports all six fields.

```json
{
  "documentId": "inv-001",
  "policyVersion": "1.0.0",
  "fields": [
    {
      "name": "total",
      "value": "12.10",
      "confidence": 0.95,
      "evidence": {
        "quote": "Total: 12.10",
        "page": 1,
        "line": 7,
        "start": 76,
        "end": 88,
        "match": "exact"
      }
    }
  ],
  "errors": [],
  "review": {"required": false}
}
```

The default policy requires `documentNumber`, `issueDate`, `currency` and
`total`, reviews any field at or below `0.80`, and checks
`subtotal + tax = total`. A different policy can be supplied with `-policy`.

## Run

```bash
# Offline, against a frozen model answer.
go run . -in testdata/example-input.json -answer testdata/example-answer.json

# Live, through the ADK agent. Needs GOOGLE_API_KEY.
export GOOGLE_API_KEY="your-key"
go run . -in testdata/example-input.json -model gemini-flash-latest
```

Both answer sources are required to be unambiguous: passing `-answer` and
`-model` together is a usage error, and a run that silently ignored `-model`
would look like a live extraction that never happened.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Fields were extracted and no review is required. |
| `1` | The run failed. |
| `2` | The command line was wrong. |
| `3` | A human must review the extraction before it is stored. |

An input this slice cannot read, such as an image or a PDF, is reported with its
own reason and exit `3` without consulting a model at all, so the explanation is
about the document rather than about a missing flag.

## Limits

These are known and deliberate, not oversights:

- **Dates are read day first.** `03/04/2026` is the third of April. A slash or
  dot separated date is genuinely ambiguous, and this slice states its
  convention instead of guessing per document.
- **An amount with more than two fraction digits is refused**, not rounded.
- **The input and the model answer are parsed strictly.** An unknown member is an
  error, because it usually means the caller expects something this command does
  not do.
- **`text/plain` is the only accepted media type.** Image and PDF inputs get an
  explicit message naming what is missing.

## Not in this slice

Named rather than silently absent, and all deferred to a later one:

- Image and PDF ingestion, an OCR engine, and page coordinate mapping. This slice
  consumes OCR text; it does not produce it.
- Destination repository adapters. Nothing here writes to an accounting or legal
  system, which is why the review gate is reported as an exit code.
- The field-validator sub-agent for ambiguous values named in the plan. Its
  ambiguity criterion belongs with the policy, and the deterministic rules cover
  the cases the fixtures describe.

## Verification

```bash
gofmt -l .
go vet ./...
go build ./...
go test ./...
```

The suite runs with no secret and no network. `testdata/documents.json` holds the
frozen cases: the document text, the answer a model returned for it, and the
audited outcome a reviewer should see. The fixtures are the specification, so the
validation layer is exercised against realistic input without a live model.

To be explicit about what was **not** observed: no Gemini credential was
available in the environment this was built in, so **the live model path was not
executed**. It is verified offline by what it constructs and what it refuses, not
by a call that happened. Reporting a check that was never run would be the first
thing this repository is supposed to prevent.

## License

MIT. See `LICENSE`.
