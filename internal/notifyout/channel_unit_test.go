package notifyout

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"testing"
)

func TestBotReady(t *testing.T) {
	tests := []struct {
		name string
		bot  Bot
		want bool
	}{
		{
			name: "disabled with empty token",
			bot:  Bot{Enabled: false, token: ""},
			want: false,
		},
		{
			name: "disabled with token",
			bot:  Bot{Enabled: false, token: "tok123"},
			want: false,
		},
		{
			name: "enabled with empty token",
			bot:  Bot{Enabled: true, token: ""},
			want: false,
		},
		{
			name: "enabled with token",
			bot:  Bot{Enabled: true, token: "tok123"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.bot.Ready(); got != tt.want {
				t.Errorf("Bot.Ready() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBotToken(t *testing.T) {
	b := Bot{token: "secret-token-xyz"}
	if got := b.Token(); got != "secret-token-xyz" {
		t.Errorf("Bot.Token() = %q, want %q", got, "secret-token-xyz")
	}
}

type dummyChannel struct {
	name string
}

func (d dummyChannel) Name() string                               { return d.name }
func (d dummyChannel) NeedsServer() bool                          { return false }
func (d dummyChannel) Check(context.Context, Bot) (string, error) { return d.name, nil }
func (d dummyChannel) Updates(context.Context, Bot, int64) ([]Update, int64, error) {
	return nil, 0, nil
}
func (d dummyChannel) Send(context.Context, Bot, string, Message) error { return nil }
func (d dummyChannel) LinkURL(Bot, string) string                       { return "" }

func TestRegisteredOrder(t *testing.T) {
	regs := Registered()
	names := make([]string, len(regs))
	for i, c := range regs {
		names[i] = c.Name()
	}

	// Default known order from channel.go: push, email, telegram, max, matrix, vk, viber, whatsapp, slack, mattermost
	expectedPrefix := []string{"push", "email", "telegram", "max", "matrix", "vk", "viber", "whatsapp", "slack", "mattermost"}
	for i, exp := range expectedPrefix {
		if i >= len(names) || names[i] != exp {
			t.Fatalf("Registered()[%d] = %v, want %s", i, names, exp)
		}
	}

	// Verify Registered returns a clone that doesn't modify internal slice
	regs[0] = nil
	regs2 := Registered()
	if regs2[0] == nil {
		t.Errorf("Registered() did not return an isolated clone")
	}

	// Registering an unknown channel appends it after the standard ones
	custom := dummyChannel{name: "custom_messenger"}
	Register(custom)
	afterReg := Registered()
	last := afterReg[len(afterReg)-1]
	if last.Name() != "custom_messenger" {
		t.Errorf("expected custom_messenger at end of Registered(), got %s", last.Name())
	}
	// Verify standard channels maintain their relative ordering
	standardIndices := make([]int, len(expectedPrefix))
	for i, ch := range expectedPrefix {
		idx := slices.IndexFunc(afterReg, func(c Channel) bool { return c.Name() == ch })
		if idx < 0 {
			t.Fatalf("channel %s not found in Registered()", ch)
		}
		standardIndices[i] = idx
	}
	for i := 1; i < len(standardIndices); i++ {
		if standardIndices[i] <= standardIndices[i-1] {
			t.Errorf("ordering broken: channel %s (index %d) appears before %s (index %d)",
				expectedPrefix[i], standardIndices[i], expectedPrefix[i-1], standardIndices[i-1])
		}
	}
}

func TestPlain(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr string
	}{
		{
			name:    "nil error",
			err:     nil,
			wantErr: "",
		},
		{
			name:    "regular error",
			err:     errors.New("simple failure"),
			wantErr: "simple failure",
		},
		{
			name: "url.Error strips sensitive URL",
			err: &url.Error{
				Op:  "Post",
				URL: "https://api.telegram.org/bot12345:SECRET/sendMessage",
				Err: errors.New("connection refused"),
			},
			wantErr: "Post: connection refused",
		},
		{
			name: "wrapped url.Error",
			err: fmt.Errorf("network call failed: %w", &url.Error{
				Op:  "Get",
				URL: "https://slack.com/api/chat.postMessage?token=secret-xoxb",
				Err: errors.New("timeout"),
			}),
			wantErr: "Get: timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plain(tt.err)
			if tt.err == nil {
				if got != nil {
					t.Errorf("plain(nil) = %v, want nil", got)
				}
				return
			}
			if got == nil || got.Error() != tt.wantErr {
				t.Errorf("plain(%v) = %v, want %q", tt.err, got, tt.wantErr)
			}
		})
	}
}
