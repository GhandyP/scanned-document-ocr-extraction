package extraction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const (
	defaultAppName      = "scanned-document-ocr-extraction"
	defaultModelName    = "gemini-flash-latest"
	extractionAgentName = "document_extractor"
	extractionUserID    = "document-extraction"
)

// extractionInstruction constrains extraction to values that can be audited
// against the OCR supplied by the caller.
const extractionInstruction = `Extract candidate fields only from the OCR text supplied in the current user message.
Copy every quote verbatim from that OCR text, preserving its original characters and spacing.
Do not invent or normalize a value that the text does not contain.
If a value cannot be quoted verbatim, omit it rather than guessing.
Return only the JSON object required by the output schema.`

// AgentConfig configures an offline-testable document extraction agent.
type AgentConfig struct {
	Model     model.LLM
	AppName   string
	ModelName string
}

// Agent runs one extraction session per document.
type Agent struct {
	root      agent.Agent
	runner    *runner.Runner
	appName   string
	modelName string
}

// NewAgent constructs an ADK agent around the caller-supplied model. It does
// not add tools, so extraction cannot perform application-side actions.
func NewAgent(cfg AgentConfig) (*Agent, error) {
	if cfg.Model == nil {
		return nil, errors.New("extraction model is required")
	}
	if cfg.AppName == "" {
		cfg.AppName = defaultAppName
	}
	modelNameWasSet := cfg.ModelName != ""
	if cfg.ModelName == "" {
		cfg.ModelName = defaultModelName
	}

	llmConfig := llmagent.Config{
		Name:            extractionAgentName,
		Description:     "Extract document fields from caller-supplied OCR text.",
		Instruction:     extractionInstruction,
		Model:           cfg.Model,
		OutputSchema:    OutputSchema(),
		OutputKey:       "candidate_answer",
		Mode:            llmagent.ModeChat,
		IncludeContents: llmagent.IncludeContentsNone,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			func(_ agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
				if modelNameWasSet || req.Model == "" {
					req.Model = cfg.ModelName
				}
				return nil, nil
			},
		},
	}
	root, err := llmagent.New(llmConfig)
	if err != nil {
		return nil, fmt.Errorf("construct extraction agent: %w", err)
	}
	adkRunner, err := runner.NewInMemory(cfg.AppName, root)
	if err != nil {
		return nil, fmt.Errorf("construct extraction runner: %w", err)
	}
	return &Agent{
		root:      root,
		runner:    adkRunner,
		appName:   cfg.AppName,
		modelName: cfg.ModelName,
	}, nil
}

// Extract runs the supplied OCR text in the session reserved for its document
// and returns only fields from the model's final JSON response.
func (a *Agent) Extract(ctx context.Context, in DocumentInput) (candidateAnswer, error) {
	if a == nil || a.runner == nil {
		return candidateAnswer{}, errors.New("extraction agent is not initialized")
	}
	if issues := in.Validate(); len(issues) != 0 {
		return candidateAnswer{}, fmt.Errorf("invalid document input: %v", issues)
	}

	message := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: in.OCRText}},
	}
	var finalEvent *session.Event
	for event, err := range a.runner.Run(ctx, extractionUserID, SessionIDFor(in.DocumentID), message, agent.RunConfig{}) {
		if err != nil {
			return candidateAnswer{}, fmt.Errorf("run extraction agent: %w", err)
		}
		if event != nil && event.IsFinalResponse() {
			finalEvent = event
		}
	}
	if finalEvent == nil {
		return candidateAnswer{}, errors.New("extraction agent produced no final response")
	}

	var response strings.Builder
	if finalEvent.Content != nil {
		for _, part := range finalEvent.Content.Parts {
			if part != nil {
				response.WriteString(part.Text)
			}
		}
	}
	if strings.TrimSpace(response.String()) == "" {
		return candidateAnswer{}, errors.New("extraction agent final response contains no JSON")
	}
	answer, err := parseCandidateAnswer([]byte(response.String()))
	if err != nil {
		return candidateAnswer{}, fmt.Errorf("parse extraction agent response: %w", err)
	}
	return answer, nil
}

// OutputSchema returns the exact shape the model may produce. Evidence and
// location metadata are deliberately absent; Go derives them from each quote.
func OutputSchema() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"fields": {
				Type: genai.TypeArray,
				Items: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"name":       {Type: genai.TypeString},
						"value":      {Type: genai.TypeString},
						"quote":      {Type: genai.TypeString},
						"confidence": {Type: genai.TypeNumber},
					},
					Required: []string{"name", "value", "quote", "confidence"},
				},
			},
		},
		Required: []string{"fields"},
	}
}

// GeminiModel is the only constructor in this package that needs credentials.
func GeminiModel(ctx context.Context, apiKey, modelName string) (model.LLM, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("Gemini API key is required")
	}
	if modelName == "" {
		modelName = defaultModelName
	}
	return gemini.NewModel(ctx, modelName, &genai.ClientConfig{APIKey: apiKey})
}

// SessionIDFor returns a deterministic, one-to-one session key for a document
// ID, so different IDs cannot address the same ADK session.
func SessionIDFor(documentID string) string {
	return "document-" + base64.RawURLEncoding.EncodeToString([]byte(documentID))
}

func parseCandidateAnswer(data []byte) (candidateAnswer, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return candidateAnswer{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if object == nil {
		return candidateAnswer{}, errors.New("response must be a JSON object")
	}
	if len(object) != 1 {
		return candidateAnswer{}, errors.New("response must contain only the fields member")
	}
	rawFields, exists := object["fields"]
	if !exists || string(rawFields) == "null" {
		return candidateAnswer{}, errors.New("response must contain a fields array")
	}

	var fields []json.RawMessage
	if err := json.Unmarshal(rawFields, &fields); err != nil {
		return candidateAnswer{}, fmt.Errorf("fields must be an array: %w", err)
	}
	if fields == nil {
		return candidateAnswer{}, errors.New("response must contain a fields array")
	}

	answer := candidateAnswer{Fields: make([]candidateField, 0, len(fields))}
	for i, rawField := range fields {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(rawField, &members); err != nil || members == nil {
			if err == nil {
				err = errors.New("field must be an object")
			}
			return candidateAnswer{}, fmt.Errorf("field %d is invalid: %w", i, err)
		}
		if len(members) != 4 {
			return candidateAnswer{}, fmt.Errorf("field %d must contain exactly name, value, quote, and confidence", i)
		}
		for _, name := range []string{"name", "value", "quote", "confidence"} {
			value, ok := members[name]
			if !ok || string(value) == "null" {
				return candidateAnswer{}, fmt.Errorf("field %d is missing required member %q", i, name)
			}
		}
		var field candidateField
		if err := json.Unmarshal(rawField, &field); err != nil {
			return candidateAnswer{}, fmt.Errorf("field %d is invalid: %w", i, err)
		}
		answer.Fields = append(answer.Fields, field)
	}

	// Decode the whole object with unknown-member rejection as a second shape
	// check, then require that the payload is exactly one JSON value.
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var decoded candidateAnswer
	if err := decoder.Decode(&decoded); err != nil {
		return candidateAnswer{}, fmt.Errorf("invalid candidate answer: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return candidateAnswer{}, errors.New("response contains more than one JSON value")
		}
		return candidateAnswer{}, fmt.Errorf("invalid trailing data: %w", err)
	}
	return answer, nil
}
