package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/i18n"
)

// A model is called with the conversation and the tools it may use; each provider speaks its own dialect: Anthropic
// Messages, OpenAI Chat Completions (also the servers compatible with OpenAI), Gemini generateContent.

// Message is one turn of a conversation: the employee's text (with the results of the tools the model asked for) or
// the model's answer (with the tools it asks for).
type Message struct {
	Role    string   `json:"role"` // "user" or "assistant"
	Text    string   `json:"text,omitempty"`
	Calls   []Call   `json:"calls,omitempty"`   // assistant: tools to call
	Results []Result `json:"results,omitempty"` // user: what the tools gave
}

// Call is a tool the model asks to call.
type Call struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Result is what a tool gave back to a call.
type Result struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Text  string `json:"text"`
	Error bool   `json:"error,omitempty"`
}

// ToolSpec describes a tool to the model; Schema is the JSON Schema of its arguments (an object).
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Reply is the model's answer with the tokens it took.
type Reply struct {
	Text      string
	Calls     []Call
	InTokens  int
	OutTokens int
}

// maxTokens bounds the answer of a model.
const maxTokens = 4096

// Ask calls the model of the connection (model "" — its default one).
func (s *Store) Ask(ctx context.Context, c Connection, key, model, system string, msgs []Message, tools []ToolSpec) (Reply, error) {
	if model == "" {
		model = c.Model
	}
	if model == "" {
		return Reply{}, i18n.E("err.ai_model_required")
	}
	switch c.Provider {
	case "anthropic":
		return s.askAnthropic(ctx, c, key, model, system, msgs, tools)
	case "google":
		return s.askGoogle(ctx, c, key, model, system, msgs, tools)
	default:
		return s.askOpenAI(ctx, c, key, model, system, msgs, tools)
	}
}

// post sends a JSON request to the provider and decodes its answer; an error carries the provider's message.
func (s *Store) post(ctx context.Context, url string, header map[string]string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	client := s.Calls
	if client == nil {
		client = s.HTTP
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d%s", res.StatusCode, providerError(data))
	}
	return json.Unmarshal(data, out)
}

// providerError is ": message" of an error answer of a provider ("" — none).
func providerError(body []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || len(e.Error) == 0 {
		return ""
	}
	var o struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Error, &o) == nil && o.Message != "" {
		return ": " + o.Message
	}
	var str string
	if json.Unmarshal(e.Error, &str) == nil && str != "" {
		return ": " + str
	}
	return ""
}

// args are the arguments of a call as an object ({} when the model gave none).
func args(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

// ---------- Anthropic Messages ----------

type anthBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

func (s *Store) askAnthropic(ctx context.Context, c Connection, key, model, system string, msgs []Message, tools []ToolSpec) (Reply, error) {
	type msg struct {
		Role    string      `json:"role"`
		Content []anthBlock `json:"content"`
	}
	type tool struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	in := struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    string `json:"system,omitempty"`
		Messages  []msg  `json:"messages"`
		Tools     []tool `json:"tools,omitempty"`
	}{Model: model, MaxTokens: maxTokens, System: system}
	for _, m := range msgs {
		var blocks []anthBlock
		for _, r := range m.Results {
			blocks = append(blocks, anthBlock{Type: "tool_result", ToolUseID: r.ID, Content: r.Text, IsError: r.Error})
		}
		if m.Text != "" {
			blocks = append(blocks, anthBlock{Type: "text", Text: m.Text})
		}
		for _, call := range m.Calls {
			blocks = append(blocks, anthBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: args(call.Args)})
		}
		in.Messages = append(in.Messages, msg{Role: m.Role, Content: blocks})
	}
	for _, t := range tools {
		in.Tools = append(in.Tools, tool{t.Name, t.Description, t.Schema})
	}
	var out struct {
		Content []anthBlock `json:"content"`
		Usage   struct {
			Input       int `json:"input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			Output      int `json:"output_tokens"`
		} `json:"usage"`
	}
	err := s.post(ctx, c.URL()+"/messages", map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}, in, &out)
	if err != nil {
		return Reply{}, err
	}
	r := Reply{InTokens: out.Usage.Input + out.Usage.CacheCreate + out.Usage.CacheRead, OutTokens: out.Usage.Output}
	var text []string
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "tool_use":
			r.Calls = append(r.Calls, Call{ID: b.ID, Name: b.Name, Args: args(b.Input)})
		}
	}
	r.Text = strings.TrimSpace(strings.Join(text, "\n"))
	return r, nil
}

// ---------- OpenAI Chat Completions (and the servers compatible with it) ----------

func (s *Store) askOpenAI(ctx context.Context, c Connection, key, model, system string, msgs []Message, tools []ToolSpec) (Reply, error) {
	type fn struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type toolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function fn     `json:"function"`
	}
	type msg struct {
		Role       string     `json:"role"`
		Content    *string    `json:"content"`
		ToolCalls  []toolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}
	type tool struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	str := func(s string) *string { return &s }
	in := struct {
		Model    string `json:"model"`
		Messages []msg  `json:"messages"`
		Tools    []tool `json:"tools,omitempty"`
	}{Model: model}
	if system != "" {
		in.Messages = append(in.Messages, msg{Role: "system", Content: str(system)})
	}
	for _, m := range msgs {
		for _, r := range m.Results {
			in.Messages = append(in.Messages, msg{Role: "tool", ToolCallID: r.ID, Content: str(r.Text)})
		}
		if m.Role == "assistant" {
			o := msg{Role: "assistant"}
			if m.Text != "" {
				o.Content = str(m.Text)
			}
			for _, call := range m.Calls {
				o.ToolCalls = append(o.ToolCalls, toolCall{ID: call.ID, Type: "function", Function: fn{call.Name, string(args(call.Args))}})
			}
			in.Messages = append(in.Messages, o)
		} else if m.Text != "" {
			in.Messages = append(in.Messages, msg{Role: "user", Content: str(m.Text)})
		}
	}
	for _, t := range tools {
		var o tool
		o.Type = "function"
		o.Function.Name, o.Function.Description, o.Function.Parameters = t.Name, t.Description, t.Schema
		in.Tools = append(in.Tools, o)
	}
	var out struct {
		Choices []struct {
			Message msg `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	header := map[string]string{}
	if key != "" {
		header["Authorization"] = "Bearer " + key
	}
	if err := s.post(ctx, c.URL()+"/chat/completions", header, in, &out); err != nil {
		return Reply{}, err
	}
	r := Reply{InTokens: out.Usage.Prompt, OutTokens: out.Usage.Completion}
	if len(out.Choices) > 0 {
		m := out.Choices[0].Message
		if m.Content != nil {
			r.Text = strings.TrimSpace(*m.Content)
		}
		for _, tc := range m.ToolCalls {
			r.Calls = append(r.Calls, Call{ID: tc.ID, Name: tc.Function.Name, Args: args(json.RawMessage(tc.Function.Arguments))})
		}
	}
	return r, nil
}

// ---------- Gemini generateContent ----------

func (s *Store) askGoogle(ctx context.Context, c Connection, key, model, system string, msgs []Message, tools []ToolSpec) (Reply, error) {
	type fcall struct {
		ID   string          `json:"id,omitempty"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args,omitempty"`
	}
	type fresp struct {
		ID       string         `json:"id,omitempty"`
		Name     string         `json:"name"`
		Response map[string]any `json:"response"`
	}
	type part struct {
		Text             string `json:"text,omitempty"`
		FunctionCall     *fcall `json:"functionCall,omitempty"`
		FunctionResponse *fresp `json:"functionResponse,omitempty"`
	}
	type content struct {
		Role  string `json:"role,omitempty"`
		Parts []part `json:"parts"`
	}
	type decl struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	in := struct {
		System   *content  `json:"systemInstruction,omitempty"`
		Contents []content `json:"contents"`
		Tools    []struct {
			FunctionDeclarations []decl `json:"functionDeclarations"`
		} `json:"tools,omitempty"`
		Config struct {
			MaxOutputTokens int `json:"maxOutputTokens"`
		} `json:"generationConfig"`
	}{}
	in.Config.MaxOutputTokens = maxTokens
	if system != "" {
		in.System = &content{Parts: []part{{Text: system}}}
	}
	for _, m := range msgs {
		o := content{Role: "user"}
		if m.Role == "assistant" {
			o.Role = "model"
		}
		for _, r := range m.Results {
			key := "result"
			if r.Error {
				key = "error"
			}
			o.Parts = append(o.Parts, part{FunctionResponse: &fresp{ID: r.ID, Name: r.Name, Response: map[string]any{key: r.Text}}})
		}
		if m.Text != "" {
			o.Parts = append(o.Parts, part{Text: m.Text})
		}
		for _, call := range m.Calls {
			o.Parts = append(o.Parts, part{FunctionCall: &fcall{ID: call.ID, Name: call.Name, Args: args(call.Args)}})
		}
		in.Contents = append(in.Contents, o)
	}
	if len(tools) > 0 {
		in.Tools = make([]struct {
			FunctionDeclarations []decl `json:"functionDeclarations"`
		}, 1)
		for _, t := range tools {
			in.Tools[0].FunctionDeclarations = append(in.Tools[0].FunctionDeclarations, decl{t.Name, t.Description, t.Schema})
		}
	}
	var out struct {
		Candidates []struct {
			Content content `json:"content"`
		} `json:"candidates"`
		Usage struct {
			Prompt     int `json:"promptTokenCount"`
			Candidates int `json:"candidatesTokenCount"`
			Thoughts   int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	u := c.URL() + "/models/" + url.PathEscape(strings.TrimPrefix(model, "models/")) + ":generateContent"
	if err := s.post(ctx, u, map[string]string{"x-goog-api-key": key}, in, &out); err != nil {
		return Reply{}, err
	}
	r := Reply{InTokens: out.Usage.Prompt, OutTokens: out.Usage.Candidates + out.Usage.Thoughts}
	if len(out.Candidates) > 0 {
		var text []string
		for i, p := range out.Candidates[0].Content.Parts {
			if p.FunctionCall != nil {
				id := p.FunctionCall.ID
				if id == "" { // Gemini may not number its calls: the answer is matched by the order
					id = fmt.Sprintf("call-%d", i)
				}
				r.Calls = append(r.Calls, Call{ID: id, Name: p.FunctionCall.Name, Args: args(p.FunctionCall.Args)})
			} else if p.Text != "" {
				text = append(text, p.Text)
			}
		}
		r.Text = strings.TrimSpace(strings.Join(text, "\n"))
	}
	return r, nil
}

// Write has the model of the user's connection answer one text with no tools (CoPilot's summary of a call and such);
// the tokens go to the usage as the agent's.
func (s *Store) Write(ctx context.Context, userID, agent, system, text string) (string, error) {
	return s.Chat(ctx, userID, agent, system, []Message{{Role: "user", Text: text}})
}

// Chat is Write for a conversation: the model of the user's connection answers its last turn (CoPilot in the
// messenger).
func (s *Store) Chat(ctx context.Context, userID, agent, system string, msgs []Message) (string, error) {
	c, key, err := s.For(ctx, userID)
	if err != nil {
		return "", err
	}
	if err := s.allow(ctx, userID, agent, c); err != nil {
		return "", err
	}
	reply, err := s.Ask(ctx, c, key, c.Model, system, msgs, nil)
	if err != nil {
		return "", i18n.E("err.ai_call", err.Error())
	}
	if err := s.note(ctx, userID, "", agent, c, c.Model, reply); err != nil {
		return "", err
	}
	return strings.TrimSpace(reply.Text), nil
}
