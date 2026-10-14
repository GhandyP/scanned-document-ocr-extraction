package extraction

import (
	"context"
	"iter"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestOutputSchemaIsCandidateAnswerOnly(t *testing.T) {
	schema := OutputSchema()
	if schema == nil {
		t.Fatal("OutputSchema() = nil")
	}
	if schema.Type != genai.TypeObject {
		t.Errorf("root type = %v, want %v", schema.Type, genai.TypeObject)
	}
	if len(schema.Properties) != 1 {
		t.Fatalf("root properties = %v, want only fields", propertyNames(schema.Properties))
	}
	fields, ok := schema.Properties["fields"]
	if !ok {
		t.Fatal("root schema has no fields property")
	}
	if fields.Type != genai.TypeArray || fields.Items == nil || fields.Items.Type != genai.TypeObject {
		t.Fatalf("fields schema = %+v, want array of objects", fields)
	}
	wantProperties := map[string]genai.Type{
		"name":       genai.TypeString,
		"value":      genai.TypeString,
		"quote":      genai.TypeString,
		"confidence": genai.TypeNumber,
	}
	if len(fields.Items.Properties) != len(wantProperties) {
		t.Fatalf("field properties = %v, want exactly %v", propertyNames(fields.Items.Properties), wantProperties)
	}
	for name, wantType := range wantProperties {
		property, ok := fields.Items.Properties[name]
		if !ok {
			t.Errorf("field schema is missing %q", name)
			continue
		}
		if property.Type != wantType {
			t.Errorf("%s type = %v, want %v", name, property.Type, wantType)
		}
	}
	if !reflect.DeepEqual(schema.Required, []string{"fields"}) {
		t.Errorf("root required = %v, want [fields]", schema.Required)
	}
	if !sameStringSet(fields.Items.Required, []string{"name", "value", "quote", "confidence"}) {
		t.Errorf("field required = %v, want name, value, quote, confidence", fields.Items.Required)
	}

	forbidden := map[string]bool{
		"evidence": true,
		"page":     true,
		"line":     true,
		"start":    true,
		"end":      true,
		"match":    true,
	}
	var walk func(*genai.Schema)
	walk = func(node *genai.Schema) {
		if node == nil {
			return
		}
		for name, child := range node.Properties {
			if forbidden[strings.ToLower(name)] {
				t.Errorf("schema contains forbidden member %q", name)
			}
			walk(child)
		}
		walk(node.Items)
		for _, child := range node.AnyOf {
			walk(child)
		}
	}
	walk(schema)
}

func TestNewAgentConfigHasNoTools(t *testing.T) {
	configured, err := NewAgent(AgentConfig{Model: &fakeModel{answer: `{"fields":[]}`, emit: true}})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	root := reflect.ValueOf(configured.root)
	if root.Kind() != reflect.Ptr || root.IsNil() {
		t.Fatalf("constructed root agent = %v, want non-nil pointer", configured.root)
	}
	llmConfig := root.Elem().FieldByName("Config")
	if !llmConfig.IsValid() || !llmConfig.CanInterface() {
		t.Fatal("constructed ADK agent does not expose its llmagent.Config for inspection")
	}
	cfg, ok := llmConfig.Interface().(llmagent.Config)
	if !ok {
		t.Fatalf("constructed config has type %T, want llmagent.Config", llmConfig.Interface())
	}
	if cfg.Tools != nil || len(cfg.Tools) != 0 {
		t.Errorf("agent config Tools = %v, want unset", cfg.Tools)
	}
}

func TestSessionIDForIsDeterministicAndDocumentScoped(t *testing.T) {
	first := SessionIDFor("invoice-1")
	if again := SessionIDFor("invoice-1"); again != first {
		t.Errorf("SessionIDFor() is not deterministic: %q != %q", first, again)
	}
	if second := SessionIDFor("invoice-2"); second == first {
		t.Errorf("different document IDs share session ID %q", first)
	}
}

func TestAgentExtractPreservesCandidateFieldsAndUsesOCRUserMessage(t *testing.T) {
	answerJSON := `{"fields":[{"name":"invoiceNumber","value":"INV-0042","quote":"Invoice No. INV-0042","confidence":0.91},{"name":"total","value":"$18.25","quote":"Total: $18.25","confidence":0.73}]}`
	fake := &fakeModel{answer: answerJSON, emit: true}
	configured, err := NewAgent(AgentConfig{Model: fake})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	got, err := configured.Extract(context.Background(), DocumentInput{
		DocumentID: "invoice-0042",
		MIMEType:   MIMETextPlain,
		OCRText:    "Invoice No. INV-0042\nTotal: $18.25",
	})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	want := CandidateAnswer{Fields: []CandidateField{
		{Name: "invoiceNumber", Value: "INV-0042", Quote: "Invoice No. INV-0042", Confidence: 0.91},
		{Name: "total", Value: "$18.25", Quote: "Total: $18.25", Confidence: 0.73},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract() = %#v, want %#v", got, want)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model calls = %d, want one", len(fake.requests))
	}
	if !requestHasUserText(fake.requests[0], "Invoice No. INV-0042\nTotal: $18.25") {
		t.Errorf("OCR text was not supplied verbatim as a user message: %+v", fake.requests[0].Contents)
	}
}

func TestAgentExtractRejectsMalformedOrMissingFinalResponse(t *testing.T) {
	tests := []struct {
		name  string
		model *fakeModel
	}{
		{name: "response is not JSON", model: &fakeModel{answer: "not JSON", emit: true}},
		{name: "required field members are not defaulted", model: &fakeModel{answer: `{"fields":[{"name":"total"}]}`, emit: true}},
		{name: "there is no final event", model: &fakeModel{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configured, err := NewAgent(AgentConfig{Model: tc.model})
			if err != nil {
				t.Fatalf("NewAgent() error = %v", err)
			}
			got, err := configured.Extract(context.Background(), DocumentInput{
				DocumentID: "invoice-invalid",
				MIMEType:   MIMETextPlain,
				OCRText:    "Total: $18.25",
			})
			if err == nil {
				t.Fatalf("Extract() = %#v, want an error", got)
			}
			if len(got.Fields) != 0 {
				t.Errorf("failed extraction returned fields: %#v", got.Fields)
			}
		})
	}
}

type fakeModel struct {
	answer   string
	emit     bool
	requests []*model.LLMRequest
}

func (*fakeModel) Name() string { return "offline-fake" }

func (m *fakeModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.requests = append(m.requests, req)
		if !m.emit {
			return
		}
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: m.answer}}},
		}, nil)
	}
}

func requestHasUserText(req *model.LLMRequest, want string) bool {
	if req == nil {
		return false
	}
	for _, content := range req.Contents {
		if content == nil || content.Role != "user" {
			continue
		}
		var text strings.Builder
		for _, part := range content.Parts {
			if part != nil {
				text.WriteString(part.Text)
			}
		}
		if text.String() == want {
			return true
		}
	}
	return false
}

func propertyNames(properties map[string]*genai.Schema) []string {
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	return names
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[string]int, len(got))
	for _, value := range got {
		set[value]++
	}
	for _, value := range want {
		if set[value] == 0 {
			return false
		}
		set[value]--
	}
	return true
}
