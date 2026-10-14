package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhandyP/scanned-document-ocr-extraction/internal/extraction"
)

type cliFixture struct {
	PolicyVersion string           `json:"policyVersion"`
	Cases         []cliFixtureCase `json:"cases"`
}

type cliFixtureCase struct {
	Name        string                     `json:"name"`
	DocumentID  string                     `json:"documentId"`
	MIMEType    string                     `json:"mimeType"`
	OCRText     string                     `json:"ocrText"`
	ModelAnswer extraction.CandidateAnswer `json:"modelAnswer"`
	Expect      cliFixtureExpect           `json:"expect"`
}

type cliFixtureExpect struct {
	Values         map[string]string `json:"values"`
	IssueCodes     []string          `json:"issueCodes"`
	ReviewRequired bool              `json:"reviewRequired"`
}

const (
	completeAnswerJSON = `{"fields":[
		{"name":"documentNumber","value":"F-1","quote":"Numero: F-1","confidence":0.95},
		{"name":"issueDate","value":"15/03/2026","quote":"Fecha: 15/03/2026","confidence":0.95},
		{"name":"currency","value":"USD","quote":"Moneda: USD","confidence":0.95},
		{"name":"subtotal","value":"10.00","quote":"Subtotal: 10.00","confidence":0.95},
		{"name":"tax","value":"2.10","quote":"IVA: 2.10","confidence":0.95},
		{"name":"total","value":"12.10","quote":"Total: 12.10","confidence":0.95}]}`

	// lowConfidenceAnswerJSON differs from the one above in exactly one field'
	// confidence, so a review it triggers can only be about the threshold and
	// not about a missing field somebody forgot to add.
	lowConfidenceAnswerJSON = `{"fields":[
		{"name":"documentNumber","value":"F-1","quote":"Numero: F-1","confidence":0.95},
		{"name":"issueDate","value":"15/03/2026","quote":"Fecha: 15/03/2026","confidence":0.95},
		{"name":"currency","value":"USD","quote":"Moneda: USD","confidence":0.95},
		{"name":"subtotal","value":"10.00","quote":"Subtotal: 10.00","confidence":0.95},
		{"name":"tax","value":"2.10","quote":"IVA: 2.10","confidence":0.95},
		{"name":"total","value":"12.10","quote":"Total: 12.10","confidence":0.30}]}`

	// completeOCRText satisfies the default policy: every required field is
	// present, the labels appear once each, and subtotal + tax really is the
	// total. Tests that are about something else use it, so the review gate is
	// not tripped by a missing field the test never meant to exercise.
	completeOCRText = "FACTURA\nNumero: F-1\nFecha: 15/03/2026\nMoneda: USD\nSubtotal: 10.00\nIVA: 2.10\nTotal: 12.10\n"
)

func loadCLIFixtures(t *testing.T) cliFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/documents.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var file cliFixture
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("the fixture file carries no cases")
	}
	return file
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func runCLI(t *testing.T, args []string, stdin string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestRunMatchesTheFixturesThroughTheCommandLine drives every frozen fixture
// through the real command line and checks the exit code, so the documented
// behaviour is verified at the surface a caller actually uses rather than only
// inside the package.
func TestRunMatchesTheFixturesThroughTheCommandLine(t *testing.T) {
	file := loadCLIFixtures(t)

	for _, tc := range file.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			inputPath := writeTemp(t, "input.json", mustJSON(t, extraction.DocumentInput{
				DocumentID: tc.DocumentID,
				MIMEType:   tc.MIMEType,
				OCRText:    tc.OCRText,
			}))
			answerPath := writeTemp(t, "answer.json", mustJSON(t, tc.ModelAnswer))

			code, stdout, stderr := runCLI(t, []string{"-in", inputPath, "-answer", answerPath}, "")

			wantCode := ExitOK
			if tc.Expect.ReviewRequired {
				wantCode = ExitReviewRequired
			}
			if code != wantCode {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, wantCode, stderr)
			}

			var got extraction.Extraction
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("output is not an Extraction: %v\n%s", err, stdout)
			}
			if len(got.Fields) != len(tc.Expect.Values) {
				t.Errorf("got %d fields, want %d", len(got.Fields), len(tc.Expect.Values))
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
			if codes := codesOf(got.Errors); len(codes) != len(tc.Expect.IssueCodes) {
				t.Errorf("issue codes = %v, want %v", codes, tc.Expect.IssueCodes)
			}
			if got.Review.Required != tc.Expect.ReviewRequired {
				t.Errorf("review required = %v, want %v", got.Review.Required, tc.Expect.ReviewRequired)
			}
			if got.Review.Required && len(got.Review.Reasons) == 0 {
				t.Error("review was required but the output carries no reason")
			}
		})
	}
}

// TestRunReportsAnUnreadableInputWithoutNeedingAModel checks the honest path: a
// media type this slice does not read is explained instead of being answered
// with "you forgot a flag", and no model is consulted.
func TestRunReportsAnUnreadableInputWithoutNeedingAModel(t *testing.T) {
	inputPath := writeTemp(t, "input.json", mustJSON(t, extraction.DocumentInput{
		DocumentID: "d-1",
		MIMEType:   extraction.MIMEImagePNG,
	}))

	code, stdout, _ := runCLI(t, []string{"-in", inputPath}, "")

	if code != ExitReviewRequired {
		t.Fatalf("exit code = %d, want %d", code, ExitReviewRequired)
	}
	var got extraction.Extraction
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("output is not an Extraction: %v\n%s", err, stdout)
	}
	if len(got.Fields) != 0 {
		t.Errorf("got %d fields from an image, want none", len(got.Fields))
	}
	if !strings.Contains(strings.Join(got.Review.Reasons, " "), "no image loader") {
		t.Errorf("review reasons do not explain the missing loader: %v", got.Review.Reasons)
	}
}

// TestRunRefusesAmbiguousAnswerSources checks that the command never quietly
// prefers one answer source over another. A run that silently ignored -model
// would look like a live extraction that never happened.
func TestRunRefusesAmbiguousAnswerSources(t *testing.T) {
	inputPath := writeTemp(t, "input.json", mustJSON(t, extraction.DocumentInput{
		DocumentID: "d-1",
		MIMEType:   extraction.MIMETextPlain,
		OCRText:    "Total: 10.00\n",
	}))

	code, stdout, stderr := runCLI(t,
		[]string{"-in", inputPath, "-answer", "answer.json", "-model", "gemini-flash-latest"}, "")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing written", stdout)
	}
	if !strings.Contains(stderr, "cannot be combined") {
		t.Errorf("stderr = %q, want it to name the conflict", stderr)
	}
}

// TestRunRequiresAnAnswerSourceForAReadableInput checks the other half of the
// same rule: with a document it could read and no way to answer, the command
// stops instead of inventing an empty extraction.
func TestRunRequiresAnAnswerSourceForAReadableInput(t *testing.T) {
	inputPath := writeTemp(t, "input.json", mustJSON(t, extraction.DocumentInput{
		DocumentID: "d-1",
		MIMEType:   extraction.MIMETextPlain,
		OCRText:    "Total: 10.00\n",
	}))

	code, stdout, stderr := runCLI(t, []string{"-in", inputPath}, "")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing written", stdout)
	}
	if !strings.Contains(stderr, "-answer") || !strings.Contains(stderr, "-model") {
		t.Errorf("stderr = %q, want it to name both answer sources", stderr)
	}
}

// TestRunHelpAndBadFlags checks that asking for help succeeds, and that an
// unusable command line is a usage failure rather than a run failure.
func TestRunHelpAndBadFlags(t *testing.T) {
	for _, flag := range []string{"-h", "-help"} {
		t.Run(flag, func(t *testing.T) {
			code, stdout, _ := runCLI(t, []string{flag}, "")
			if code != ExitOK {
				t.Errorf("exit code = %d, want %d", code, ExitOK)
			}
			for _, want := range []string{"Usage:", "-answer", "-model", "Exit codes:", "text/plain"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("help output does not mention %q", want)
				}
			}
		})
	}

	t.Run("an unknown flag", func(t *testing.T) {
		if code, _, _ := runCLI(t, []string{"-nope"}, ""); code != ExitUsage {
			t.Errorf("exit code = %d, want %d", code, ExitUsage)
		}
	})
	t.Run("a stray argument", func(t *testing.T) {
		if code, _, _ := runCLI(t, []string{"extra"}, ""); code != ExitUsage {
			t.Errorf("exit code = %d, want %d", code, ExitUsage)
		}
	})
	t.Run("malformed input JSON", func(t *testing.T) {
		inputPath := writeTemp(t, "input.json", "{not json")
		if code, _, _ := runCLI(t, []string{"-in", inputPath, "-answer", "a.json"}, ""); code != ExitFailure {
			t.Errorf("exit code = %d, want %d", code, ExitFailure)
		}
	})
	t.Run("an unknown input member is refused", func(t *testing.T) {
		inputPath := writeTemp(t, "input.json",
			`{"documentId":"d-1","mimeType":"text/plain","ocrText":"x","confidence":1}`)
		code, _, stderr := runCLI(t, []string{"-in", inputPath, "-answer", "a.json"}, "")
		if code != ExitFailure {
			t.Errorf("exit code = %d, want %d", code, ExitFailure)
		}
		if !strings.Contains(stderr, "confidence") {
			t.Errorf("stderr = %q, want it to name the unknown member", stderr)
		}
	})
}

// TestRunReadsStdinAndWritesAFile checks the two stream ends of the contract,
// because a caller scripting this command depends on both.
func TestRunReadsStdinAndWritesAFile(t *testing.T) {
	answerPath := writeTemp(t, "answer.json", completeAnswerJSON)
	outPath := filepath.Join(t.TempDir(), "out.json")

	code, stdout, stderr := runCLI(t,
		[]string{"-in", "-", "-answer", answerPath, "-out", outPath},
		mustJSON(t, extraction.DocumentInput{
			DocumentID: "d-1",
			MIMEType:   extraction.MIMETextPlain,
			OCRText:    completeOCRText,
		}))

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing when -out names a file", stdout)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	var got extraction.Extraction
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("output file is not an Extraction: %v", err)
	}
	field, ok := got.Field(extraction.FieldTotal)
	if !ok || field.Value != "12.10" {
		t.Errorf("total = %+v (present %v), want 12.10", field, ok)
	}
}

// TestBinaryExitCodes runs the compiled command, so the real process exit codes
// are observed rather than only the value the in-process function returned.
func TestBinaryExitCodes(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "scanned-document-ocr-extraction")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	inputPath := writeTemp(t, "input.json", mustJSON(t, extraction.DocumentInput{
		DocumentID: "d-1",
		MIMEType:   extraction.MIMETextPlain,
		OCRText:    completeOCRText,
	}))
	answerPath := writeTemp(t, "answer.json", completeAnswerJSON)
	lowPath := writeTemp(t, "low.json", lowConfidenceAnswerJSON)
	imagePath := writeTemp(t, "image.json", mustJSON(t, extraction.DocumentInput{
		DocumentID: "d-1",
		MIMEType:   extraction.MIMEImageJPEG,
	}))

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"readable and above review", []string{"-in", inputPath, "-answer", answerPath}, ExitOK},
		{"below the review threshold", []string{"-in", inputPath, "-answer", lowPath}, ExitReviewRequired},
		{"an unreadable document", []string{"-in", imagePath}, ExitReviewRequired},
		{"no answer source", []string{"-in", inputPath}, ExitUsage},
		{"help", []string{"-h"}, ExitOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binary, tc.args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()

			code := 0
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("run binary: %v", err)
			}
			if code != tc.want {
				t.Errorf("process exit code = %d, want %d (stderr: %s)", code, tc.want, stderr.String())
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func codesOf(issues []extraction.Issue) []string {
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	return codes
}
