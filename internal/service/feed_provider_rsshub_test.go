package service

import "testing"

func TestBuildRSSHubFeedURLSupportsVideoPlatforms(t *testing.T) {
	tests := []struct {
		name     string
		template string
		params   map[string]string
		want     string
	}{
		{"youtube channel", "youtube/channel", map[string]string{"id": "UC123"}, "https://rsshub.app/youtube/channel/UC123"},
		{"youtube playlist", "youtube/playlist", map[string]string{"id": "PL123"}, "https://rsshub.app/youtube/playlist/PL123"},
		{"bilibili user", "bilibili/user/video", map[string]string{"id": "2267573"}, "https://rsshub.app/bilibili/user/video/2267573"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildRSSHubFeedURL(test.template, test.params)
			if err != nil {
				t.Fatalf("build URL: %v", err)
			}
			if got != test.want {
				t.Fatalf("URL=%q, want %q", got, test.want)
			}
		})
	}
}
