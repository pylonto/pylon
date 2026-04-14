package channel

import "testing"

func TestMarkdownToSlackMrkdwn(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain text", in: "Hello world", want: "Hello world"},
		{name: "bold", in: "**bold**", want: "*bold*"},
		{name: "italic", in: "*italic*", want: "_italic_"},
		{name: "strikethrough", in: "~~strike~~", want: "~strike~"},
		{name: "inline code", in: "`code`", want: "`code`"},
		{name: "bold and italic", in: "**bold** and *italic*", want: "*bold* and _italic_"},
		{
			name: "fenced code block strips language",
			in:   "```go\nfmt.Println(\"hello\")\n```",
			want: "```\nfmt.Println(\"hello\")\n```",
		},
		{
			name: "fenced code block no language",
			in:   "```\nplain code\n```",
			want: "```\nplain code\n```",
		},
		{
			name: "code block with special chars",
			in:   "```\n**not bold** _not italic_\n```",
			want: "```\n**not bold** _not italic_\n```",
		},
		{
			name: "link",
			in:   "[click here](https://example.com)",
			want: "<https://example.com|click here>",
		},
		{name: "heading 1", in: "# Title", want: "*Title*"},
		{name: "heading 2", in: "## Subtitle", want: "*Subtitle*"},
		{
			name: "unordered list",
			in:   "- one\n- two\n- three",
			want: "- one\n- two\n- three",
		},
		{
			name: "ordered list",
			in:   "1. first\n2. second\n3. third",
			want: "1. first\n2. second\n3. third",
		},
		{
			name: "blockquote",
			in:   "> quoted text",
			want: "> quoted text",
		},
		{name: "thematic break", in: "---", want: "---"},
		{
			name: "mixed inline",
			in:   "This is **bold** with `code` and a [link](https://x.com)",
			want: "This is *bold* with `code` and a <https://x.com|link>",
		},
		{
			name: "paragraphs",
			in:   "First paragraph.\n\nSecond paragraph.",
			want: "First paragraph.\n\nSecond paragraph.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownToSlackMrkdwn(tt.in)
			if got != tt.want {
				t.Errorf("MarkdownToSlackMrkdwn(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMarkdownToTelegramV2(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain text", in: "Hello world", want: "Hello world"},
		{
			name: "escapes special chars",
			in:   "Price: $100 (estimated).",
			want: `Price: $100 \(estimated\)\.`,
		},
		{name: "bold", in: "**bold**", want: "*bold*"},
		{name: "italic", in: "*italic*", want: "_italic_"},
		{name: "strikethrough", in: "~~strike~~", want: "~strike~"},
		{name: "inline code no escape", in: "`a.b!c`", want: "`a.b!c`"},
		{
			name: "fenced code block keeps language",
			in:   "```go\nfmt.Println(\"hello\")\n```",
			want: "```go\nfmt.Println(\"hello\")\n```",
		},
		{
			name: "code block no escaping inside",
			in:   "```\n**not bold** _not italic_\n```",
			want: "```\n**not bold** _not italic_\n```",
		},
		{
			name: "link",
			in:   "[click here](https://example.com)",
			want: "[click here](https://example.com)",
		},
		{
			name: "link with special url chars",
			in:   "[text](https://example.com/path?a=1)",
			want: `[text](https://example.com/path?a=1)`,
		},
		{name: "heading", in: "# Title", want: "*Title*"},
		{
			name: "heading with special chars",
			in:   "# Hello!",
			want: `*Hello\!*`,
		},
		{
			name: "unordered list",
			in:   "- one\n- two",
			want: "\\- one\n\\- two",
		},
		{
			name: "ordered list",
			in:   "1. first\n2. second",
			want: "1\\. first\n2\\. second",
		},
		{
			name: "blockquote",
			in:   "> quoted text",
			want: ">quoted text",
		},
		{
			name: "thematic break",
			in:   "---",
			want: `\-\-\-`,
		},
		{
			name: "plain text with dashes",
			in:   "Queued -- 2/3 slots.",
			want: `Queued \-\- 2/3 slots\.`,
		},
		{
			name: "backtick commands",
			in:   "Commands: `done`  `status`  `help`",
			want: "Commands: `done`  `status`  `help`",
		},
		{
			name: "paragraphs",
			in:   "First.\n\nSecond.",
			want: "First\\.\n\nSecond\\.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownToTelegramV2(tt.in)
			if got != tt.want {
				t.Errorf("MarkdownToTelegramV2(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEscapeTelegramText(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"hello", "hello"},
		{"a.b", `a\.b`},
		{"a!b", `a\!b`},
		{"(test)", `\(test\)`},
		{"no-op", `no\-op`},
		{"", ""},
	}
	for _, tt := range tests {
		got := escapeTelegramText(tt.in)
		if got != tt.want {
			t.Errorf("escapeTelegramText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEscapeTelegramURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://example.com", "https://example.com"},
		{"https://example.com/path)", `https://example.com/path\)`},
		{`path\end`, `path\\end`},
	}
	for _, tt := range tests {
		got := escapeTelegramURL(tt.in)
		if got != tt.want {
			t.Errorf("escapeTelegramURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
