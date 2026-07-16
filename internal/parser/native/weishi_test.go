package native

import "testing"

func TestWeiShiExtractVideoID(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{
			name:   "query id",
			rawURL: "https://isee.weishi.qq.com/ws/app-pages/share/index.html?id=vfD4rz6U",
			want:   "vfD4rz6U",
		},
		{
			name:   "short path",
			rawURL: "https://video.weishi.qq.com/vfD4rz6U",
			want:   "vfD4rz6U",
		},
		{
			name:    "sorry page is not an id",
			rawURL:  "https://video.weishi.qq.com/sorry?from=vfD4rz6U",
			wantErr: true,
		},
	}

	parser := weiShi{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.extractVideoID(tt.rawURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("extractVideoID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("extractVideoID() = %q, want %q", got, tt.want)
			}
		})
	}
}
