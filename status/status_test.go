package status

import (
	"testing"

	"verbose-fortnight/config"
)

func TestShouldStartServer(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil config",
			cfg:  nil,
			want: false,
		},
		{
			name: "flag disabled",
			cfg: &config.Config{
				EnableStatusServer: false,
				StatusAddr:         "127.0.0.1:6061",
			},
			want: false,
		},
		{
			name: "empty addr",
			cfg: &config.Config{
				EnableStatusServer: true,
				StatusAddr:         "",
			},
			want: false,
		},
		{
			name: "off addr",
			cfg: &config.Config{
				EnableStatusServer: true,
				StatusAddr:         "off",
			},
			want: false,
		},
		{
			name: "disabled addr",
			cfg: &config.Config{
				EnableStatusServer: true,
				StatusAddr:         "disabled",
			},
			want: false,
		},
		{
			name: "valid",
			cfg: &config.Config{
				EnableStatusServer: true,
				StatusAddr:         "127.0.0.1:6061",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldStartServer(tt.cfg)
			if got != tt.want {
				t.Fatalf("ShouldStartServer()=%t want=%t", got, tt.want)
			}
		})
	}
}
