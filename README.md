# scanned-document-ocr-extraction

Extract typed fields from supplied document or OCR text, keep every value
attached to the exact text it came from, and write no record without a human
review when confidence is below policy.

Built with **Go 1.26** and **Google ADK Go v2** (Gemini).

This repository is a text-only extraction contract. It does **not** read a scan,
an image or a PDF, and it does **not** run OCR. It takes OCR text that something
else produced, extracts fields from it, and refuses to believe anything that text
does not support.

> Status: under active construction. The extraction contract, the deterministic
> validation rules and the ADK agent layer are being built in bounded work units.

## Why

Reading an invoice or a form out of noisy OCR text is a place where a confident
wrong answer is expensive. Three failure modes dominate:

1. The model returns a plausible total that appears nowhere in the text.
2. A currency or a date is normalised into a value the document never stated.
3. A low-confidence field is treated as settled and reaches an accounting record.

This repository is built so all three are structurally hard.

## Scope

In scope: a strict extraction contract, deterministic validation in pure Go
(required fields, formats, arithmetic, repeated values, OCR substitutions), a
schema-constrained ADK agent, and a review queue gate.

Out of scope for this slice: image and PDF ingestion, an OCR engine, page
coordinate mapping, and destination repository adapters.

## Build

```bash
go build ./...
go vet ./...
go test ./...
```

## License

MIT. See `LICENSE`.
