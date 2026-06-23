package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/pelletier/go-toml/v2"

	"google.golang.org/genai"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/tool"

	tapeagent "github.com/scbizu/tape-go/pkg/agent"
	agenttools "github.com/scbizu/tape-go/pkg/agent/tools"
	"github.com/scbizu/tape-go/pkg/provider/ds"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
)

const (
	appName   = "tape-e2e"
	ownerID   = "demo-user"
	sessionID = "demo-session"
)

func main() {
	ctx := owner.WithOwnerId(context.Background(), ownerID)
	apiKey, err := deepSeekAPIKey(configPath())
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "chat" {
		if err := runChat(ctx, apiKey); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := runRewindDemo(ctx, apiKey); err != nil {
		log.Fatal(err)
	}
}

func runRewindDemo(ctx context.Context, apiKey string) error {
	dir, err := os.MkdirTemp("", "tape-go-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	store, err := jsonl.NewJSONLStorage(sessionID, dir)
	if err != nil {
		return err
	}
	if err := store.Init(ctx); err != nil {
		return err
	}
	t := &tape.Tape{TapeStorage: store, OwnerID: ownerID}

	if err := t.Store(ctx, entry.NewEntry(
		entry.WithEntryKind(entry.EntryUser),
		entry.WithEntryContent("The archived reference is entry one."),
	)); err != nil {
		return err
	}
	if err := t.HandOff(ctx, tape.WithHandoffSummary("Archived demo context")); err != nil {
		return err
	}

	r, err := newRunner(apiKey, t, "Always call rewind before answering. Report the returned entry range.")
	if err != nil {
		return err
	}

	message := genai.NewContentFromText(
		"Use rewind with max_anchors=1, then tell me which archived range it found.",
		genai.RoleUser,
	)
	return runOnce(ctx, r, message)
}

func runChat(ctx context.Context, apiKey string) error {
	dir, err := os.MkdirTemp("", "tape-go-chat-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	store, err := jsonl.NewJSONLStorage(sessionID, dir)
	if err != nil {
		return err
	}
	if err := store.Init(ctx); err != nil {
		return err
	}
	t := &tape.Tape{TapeStorage: store, OwnerID: ownerID}
	r, err := newRunner(apiKey, t, "You are a concise CLI assistant. Use rewind when older tape context is needed.")
	if err != nil {
		return err
	}

	_, err = tea.NewProgram(newChatUI(ctx, r)).Run()
	return err
}

func newRunner(apiKey string, t *tape.Tape, instruction string) (*runner.Runner, error) {
	adapter, err := tapeagent.NewTapeAdapter(t, appName)
	if err != nil {
		return nil, err
	}
	commands := tapeagent.NewCommandRegistry(tapeagent.BuiltinBashCommand(), agenttools.NewRewindCommand(t))
	rewindTool, err := agenttools.NewRewindTool(commands)
	if err != nil {
		return nil, err
	}
	model, err := ds.NewModel(apiKey, os.Getenv("DEEPSEEK_MODEL"))
	if err != nil {
		return nil, err
	}
	runtime, err := tapeagent.NewRuntime(llmagent.Config{
		Name:                 "tape_demo_agent",
		Model:                model,
		Instruction:          instruction,
		Tools:                []tool.Tool{rewindTool},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{adapter.ContextWindow},
	}, tapeagent.WithCommandRegistry(commands))
	if err != nil {
		return nil, err
	}
	return runner.New(runner.Config{
		AppName:        appName,
		Agent:          runtime.Agent,
		SessionService: adapter,
		MemoryService:  adapter,
	})
}

func runOnce(ctx context.Context, r *runner.Runner, message *genai.Content) error {
	for event, err := range r.Run(ctx, ownerID, sessionID, message, adkagent.RunConfig{}) {
		if err != nil {
			return err
		}
		printEvent(event.Content)
	}
	return nil
}

type chatUI struct {
	ctx   context.Context
	r     *runner.Runner
	input textinput.Model
	lines []string
	busy  bool
}

type chatResult struct {
	text string
	err  error
}

func newChatUI(ctx context.Context, r *runner.Runner) chatUI {
	input := textinput.New()
	input.Prompt = "> "
	input.Focus()
	return chatUI{
		ctx:   ctx,
		r:     r,
		input: input,
		lines: []string{"chat ready. /exit quits."},
	}
}

func (m chatUI) Init() tea.Cmd {
	return textinput.Blink
}

func (m chatUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case chatResult:
		m.busy = false
		if msg.err != nil {
			m.lines = append(m.lines, "error: "+msg.err.Error())
		} else if msg.text != "" {
			m.lines = append(m.lines, msg.text)
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.busy {
				return m, nil
			}
			if text == "/exit" || text == "/quit" {
				return m, tea.Quit
			}
			m.input.Reset()
			m.busy = true
			m.lines = append(m.lines, "> "+text)
			return m, m.send(text)
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m chatUI) View() string {
	out := strings.Join(m.lines, "\n")
	if m.busy {
		out += "\n..."
	}
	return out + "\n" + m.input.View() + "\n"
}

func (m chatUI) send(text string) tea.Cmd {
	return func() tea.Msg {
		var b strings.Builder
		message := genai.NewContentFromText(text, genai.RoleUser)
		for event, err := range m.r.Run(m.ctx, ownerID, sessionID, message, adkagent.RunConfig{}) {
			if err != nil {
				return chatResult{err: err}
			}
			writeEvent(&b, event.Content)
		}
		return chatResult{text: strings.TrimSpace(b.String())}
	}
}

func deepSeekAPIKey(path string) (string, error) {
	if apiKey := os.Getenv("DEEPSEEK_API_KEY"); apiKey != "" {
		return apiKey, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read DeepSeek config %s: %w", path, err)
	}
	var config struct {
		Provider struct {
			DeepSeek struct {
				APIKey string `toml:"api_key"`
			} `toml:"deepseek"`
		} `toml:"provider"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse DeepSeek config %s: %w", path, err)
	}
	if config.Provider.DeepSeek.APIKey == "" {
		return "", fmt.Errorf("DEEPSEEK_API_KEY or provider.deepseek.api_key in %s is required", path)
	}
	return config.Provider.DeepSeek.APIKey, nil
}

func configPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "config.toml")
}

func printEvent(content *genai.Content) {
	writeEvent(os.Stdout, content)
}

func writeEvent(w interface{ Write([]byte) (int, error) }, content *genai.Content) {
	if content == nil {
		return
	}
	for _, part := range content.Parts {
		switch {
		case part.FunctionCall != nil:
			fmt.Fprintf(w, "tool call: %s %v\n", part.FunctionCall.Name, part.FunctionCall.Args)
		case part.FunctionResponse != nil:
			fmt.Fprintf(w, "tool response: %s %v\n", part.FunctionResponse.Name, part.FunctionResponse.Response)
		case part.Text != "":
			fmt.Fprintln(w, part.Text)
		}
	}
}
