package core

import "testing"

func TestRedactToolText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bearer header in curl",
			in:   `curl -H "Authorization: Bearer abc123" https://x`,
			want: `curl -H "Authorization: Bearer [redacted]" https://x`,
		},
		{
			name: "env assignment before command",
			in:   `API_TOKEN=abc123 content publish 42`,
			want: `API_TOKEN=[redacted] content publish 42`,
		},
		{
			name: "credentials in URL",
			in:   `git clone https://user:pw@host/repo`,
			want: `git clone https://[redacted]@host/repo`,
		},
		{
			name: "JSON secret key",
			in:   `{"api_key":"abc","query":"x"}`,
			want: `{"api_key":"[redacted]","query":"x"}`,
		},
		{
			name: "password flag",
			in:   `login --password hunter2 --user bob`,
			want: `login --password [redacted] --user bob`,
		},
		{
			name: "no secret",
			in:   `ls -la /tmp`,
			want: `ls -la /tmp`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactToolText(tt.in); got != tt.want {
				t.Errorf("RedactToolText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
