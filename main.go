// Command scanned-document-ocr-extraction extracts typed fields from OCR text
// that some other system produced.
//
// It reads a DocumentInput as JSON, obtains the fields either from the ADK agent
// or from a frozen answer the caller supplies, validates them against the OCR
// text in Go, writes the Extraction as JSON, and says whether a human has to
// look at the result before it is stored.
//
// It never reads a scan, an image or a PDF. A media type it cannot read is
// reported as out of scope and produces no field at all, and it never becomes a
// record: this command writes a report and nothing else.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/GhandyP/scanned-document-ocr-extraction/internal/extraction"
)

// Exit codes. ExitReviewRequired exists so a caller can tell "these fields are
// readable but a human has to confirm them" apart from "this run failed".
const (
	ExitOK             = 0
	ExitFailure        = 1
	ExitUsage          = 2
	ExitReviewRequired = 3
)

const usage = `scanned-document-ocr-extraction extracts typed fields from OCR text.

Usage:
  scanned-document-ocr-extraction [flags]

Flags:
  -in string       path to a DocumentInput JSON file, or - for stdin (default "-")
  -out string      path for the Extraction JSON, or - for stdout (default "-")
  -answer string   path to a frozen candidate answer JSON, for an offline run
  -model string    name of the Gemini model to run the ADK agent with
  -policy string   path to a Policy JSON instead of the built-in invoice policy
  -h, -help        show this help

Exactly one of -answer and -model is required for a document that can be read.
An input that cannot be read is reported without consulting a model at all, so
its reason is about the document rather than about a missing flag.

Input contract:
  {"documentId":"inv-001","mimeType":"text/plain","ocrText":"Total: 1,500.40"}

Answer contract, which -answer reads and which is the shape the model is asked
for; it carries no evidence, because Go derives every span by locating the quote:
  {"fields":[{"name":"total","value":"1,500.40","quote":"Total: 1,500.40",
   "confidence":0.96}]}

Output contract:
  {"documentId","policyVersion","fields":[{"name","value","confidence",
   "evidence":{"quote","page","line","start","end","match"}}],"errors":[
   {"code","severity","field","message"}],"review":{"required","reasons"}}

Exit codes:
  0  fields were extracted and no review is required
  1  the run failed
  2  the command line was wrong
  3  a human must review the extraction before it is stored
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("scanned-document-ocr-extraction", flag.ContinueOnError)
	flags.SetOutput(stderr)

	inPath := flags.String("in", "-", "path to the input JSON, or - for stdin")
	outPath := flags.String("out", "-", "path for the output JSON, or - for stdout")
	answerPath := flags.String("answer", "", "path to a frozen candidate answer JSON")
	modelName := flags.String("model", "", "name of the Gemini model to run the ADK agent with")
	policyPath := flags.String("policy", "", "path to a Policy JSON")
	help := flags.Bool("help", false, "show this help")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return ExitOK
		}
		return ExitUsage
	}
	if *help {
		fmt.Fprint(stdout, usage)
		return ExitOK
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", flags.Arg(0))
		return ExitUsage
	}

	// A run that quietly preferred one answer source would look like a live
	// extraction that never happened, so the combination is refused up front.
	if *answerPath != "" && *modelName != "" {
		fmt.Fprintln(stderr, "-answer and -model cannot be combined: choose where the fields come from")
		return ExitUsage
	}

	policy, ok := loadPolicy(*policyPath, stderr)
	if !ok {
		return ExitFailure
	}

	raw, err := readSource(*inPath, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "read input: %v\n", err)
		return ExitFailure
	}
	var in extraction.DocumentInput
	if err := decodeStrict(raw, &in); err != nil {
		fmt.Fprintf(stderr, "parse input: %v\n", err)
		return ExitFailure
	}

	// An input the contract requires but the caller left incomplete is reported
	// as a whole, without a model call, because a field that cannot be keyed to
	// a document cannot enter a review queue either.
	if len(in.Validate()) != 0 {
		return report(extraction.Evaluate(in, extraction.CandidateAnswer{}, policy),
			*outPath, stdout, stderr)
	}

	if *answerPath == "" && *modelName == "" {
		fmt.Fprintln(stderr, "exactly one of -answer and -model is required to obtain the fields")
		return ExitUsage
	}

	answer, code := obtainAnswer(*answerPath, *modelName, in, stderr)
	if code != ExitOK {
		return code
	}

	return report(extraction.Evaluate(in, answer, policy), *outPath, stdout, stderr)
}

// obtainAnswer returns the candidate answer and, when it could not be obtained,
// the exit code that describes why.
func obtainAnswer(path, modelName string, in extraction.DocumentInput, stderr io.Writer) (extraction.CandidateAnswer, int) {
	if path != "" {
		raw, err := readSource(path, nil)
		if err != nil {
			fmt.Fprintf(stderr, "read answer: %v\n", err)
			return extraction.CandidateAnswer{}, ExitFailure
		}
		var answer extraction.CandidateAnswer
		if err := decodeStrict(raw, &answer); err != nil {
			fmt.Fprintf(stderr, "parse answer: %v\n", err)
			return extraction.CandidateAnswer{}, ExitFailure
		}
		return answer, ExitOK
	}

	answer, err := runModel(context.Background(), in, modelName)
	if err != nil {
		fmt.Fprintf(stderr, "run model: %v\n", err)
		return extraction.CandidateAnswer{}, ExitFailure
	}
	return answer, ExitOK
}

func runModel(ctx context.Context, in extraction.DocumentInput, modelName string) (extraction.CandidateAnswer, error) {
	apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	if apiKey == "" {
		return extraction.CandidateAnswer{}, errors.New("GOOGLE_API_KEY is required to run the live model; use -answer for an offline run")
	}

	llm, err := extraction.GeminiModel(ctx, apiKey, modelName)
	if err != nil {
		return extraction.CandidateAnswer{}, err
	}
	agent, err := extraction.NewAgent(extraction.AgentConfig{Model: llm, ModelName: modelName})
	if err != nil {
		return extraction.CandidateAnswer{}, err
	}
	return agent.Extract(ctx, in)
}

// report writes the extraction and turns the review gate into the exit code. The
// extraction is checked against its own shape first, so a malformed result never
// reaches a caller.
func report(result extraction.Extraction, outPath string, stdout, stderr io.Writer) int {
	if issues := result.Validate(); len(issues) != 0 {
		fmt.Fprintf(stderr, "internal error: the extraction fails its own shape check: %v\n", issues)
		return ExitFailure
	}

	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "encode extraction: %v\n", err)
		return ExitFailure
	}
	raw = append(raw, '\n')

	if err := writeSink(outPath, raw, stdout); err != nil {
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return ExitFailure
	}

	if result.Review.Required {
		return ExitReviewRequired
	}
	return ExitOK
}

func loadPolicy(path string, stderr io.Writer) (extraction.Policy, bool) {
	if path == "" {
		return extraction.DefaultPolicy(), true
	}
	raw, err := readSource(path, nil)
	if err != nil {
		fmt.Fprintf(stderr, "read policy: %v\n", err)
		return extraction.Policy{}, false
	}
	var policy extraction.Policy
	if err := decodeStrict(raw, &policy); err != nil {
		fmt.Fprintf(stderr, "parse policy: %v\n", err)
		return extraction.Policy{}, false
	}
	if issues := policy.Validate(); len(issues) != 0 {
		fmt.Fprintf(stderr, "the policy is not usable: %v\n", issues)
		return extraction.Policy{}, false
	}
	return policy, true
}

func readSource(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		if stdin == nil {
			return nil, errors.New("this path cannot be read from stdin")
		}
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path)
}

func writeSink(path string, data []byte, stdout io.Writer) error {
	if path == "-" {
		_, err := stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// decodeStrict refuses a document with a member this contract does not define,
// because an unknown member means the caller expects something this command does
// not do.
func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("input contains more than one JSON value")
		}
		return fmt.Errorf("invalid trailing data: %w", err)
	}
	return nil
}
