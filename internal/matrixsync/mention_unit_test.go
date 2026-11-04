package matrixsync

import "testing"

// TestMentions tests mentions() checking for room-level and user-specific Matrix mentions.
func TestMentions(t *testing.T) {
	tests := []struct {
		name    string
		content map[string]any
		mxid    string
		want    bool
	}{
		{
			name:    "nil content",
			content: nil,
			mxid:    "@alice:example.com",
			want:    false,
		},
		{
			name:    "empty content",
			content: map[string]any{},
			mxid:    "@alice:example.com",
			want:    false,
		},
		{
			name: "room mention true",
			content: map[string]any{
				"m.mentions": map[string]any{"room": true},
			},
			mxid: "@alice:example.com",
			want: true,
		},
		{
			name: "room mention false with matching user_ids",
			content: map[string]any{
				"m.mentions": map[string]any{
					"room":     false,
					"user_ids": []any{"@alice:example.com"},
				},
			},
			mxid: "@alice:example.com",
			want: true,
		},
		{
			name: "room mention false with non-matching user_ids",
			content: map[string]any{
				"m.mentions": map[string]any{
					"room":     false,
					"user_ids": []any{"@bob:example.com"},
				},
			},
			mxid: "@alice:example.com",
			want: false,
		},
		{
			name: "user_ids contains target among multiple users",
			content: map[string]any{
				"m.mentions": map[string]any{
					"user_ids": []any{"@bob:example.com", "@alice:example.com", "@carol:example.com"},
				},
			},
			mxid: "@alice:example.com",
			want: true,
		},
		{
			name: "user_ids does not contain target",
			content: map[string]any{
				"m.mentions": map[string]any{
					"user_ids": []any{"@bob:example.com", "@carol:example.com"},
				},
			},
			mxid: "@alice:example.com",
			want: false,
		},
		{
			name: "user_ids empty slice",
			content: map[string]any{
				"m.mentions": map[string]any{
					"user_ids": []any{},
				},
			},
			mxid: "@alice:example.com",
			want: false,
		},
		{
			name: "user_ids contains non-string elements",
			content: map[string]any{
				"m.mentions": map[string]any{
					"user_ids": []any{123, true, nil, "@alice:example.com"},
				},
			},
			mxid: "@alice:example.com",
			want: true,
		},
		{
			name: "m.mentions not a map",
			content: map[string]any{
				"m.mentions": "invalid",
			},
			mxid: "@alice:example.com",
			want: false,
		},
		{
			name: "m.mentions user_ids is []string instead of []any",
			// Note: mention.go line 133 casts to []any, so Go []string yields false.
			content: map[string]any{
				"m.mentions": map[string]any{
					"user_ids": []string{"@alice:example.com"},
				},
			},
			mxid: "@alice:example.com",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mentions(tt.content, tt.mxid)
			if got != tt.want {
				t.Errorf("mentions() = %v, want %v", got, tt.want)
			}
		})
	}
}
