package parser

import "testing"

func TestSourceForShareURLUsesHostnameBoundaries(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "https://h5.pipix.com/item/7111320087503575303", want: SourcePiPiXia},
		{raw: "https://h5.pipigx.com/pp/post/808595934847", want: SourcePiPiGaoXiao},
		{raw: "https://x.com/Eminem/status/943590594491772928", want: SourceTwitter},
		{raw: "https://video.weishi.qq.com/vfD4rz6U", want: SourceWeiShi},
		{raw: "https://isee.weishi.qq.com/ws/app-pages/share/index.html?id=vfD4rz6U", want: SourceWeiShi},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			if got := sourceForShareURL(tc.raw); got != tc.want {
				t.Fatalf("sourceForShareURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
