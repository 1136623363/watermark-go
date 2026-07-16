package native

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestWeiBoParsesSyntheticSnapshotWithoutNetwork(t *testing.T) {
	w := weiBo{}
	data := gjson.Parse(`{
  "text":"<p>synthetic post</p>",
  "user":{"screen_name":"fixture-author","avatar_large":"https://cdn.example/avatar.jpg"},
  "pics":[{"large":{"url":"https://cdn.example/image.jpg"}}],
  "page_info":{"media_info":{"stream_url_hd":"https://cdn.example/video.mp4"},"page_pic":"https://cdn.example/cover.jpg"}
}`)
	got, err := w.parseMobileApiData(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "synthetic post" || got.Author.Name != "fixture-author" || len(got.Images) != 1 {
		t.Fatalf("unexpected synthetic Weibo result: %#v", got)
	}
	if _, err := w.parseShareUrl("https://example.com/invalid"); err == nil {
		t.Fatal("invalid host-shaped path was accepted")
	}
}

func TestWeiBo_cleanText(t *testing.T) {
	w := weiBo{}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Text with HTML tags",
			input:    "<span class=\"text\">Hello World</span>",
			expected: "Hello World",
		},
		{
			name:     "Text with multiple tags",
			input:    "<div><p>Hello <strong>World</strong></p></div>",
			expected: "Hello World",
		},
		{
			name:     "Plain text",
			input:    "Hello World",
			expected: "Hello World",
		},
		{
			name:     "Text with whitespace",
			input:    "  Hello World  ",
			expected: "Hello World",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := w.cleanText(tt.input)
			if got != tt.expected {
				t.Errorf("weiBo.cleanText() = %v, want %v", got, tt.expected)
			}
		})
	}
}
