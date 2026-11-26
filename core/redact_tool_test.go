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
		{
			name: "short flag that is not a password",
			in:   `mkdir -p dir`,
			want: `mkdir -p dir`,
		},
		{
			name: "api key header in curl",
			in:   `curl -H "X-Api-Key: sk-live-123" https://x`,
			want: `curl -H "X-Api-Key: [redacted]" https://x`,
		},
		{
			name: "auth token header",
			in:   `curl -H 'X-Auth-Token: abc' https://x`,
			want: `curl -H 'X-Auth-Token: [redacted]' https://x`,
		},
		{
			name: "cookie header",
			in:   `Cookie: sid=abc`,
			want: `Cookie: [redacted]`,
		},
		{
			name: "cookie header with several cookies",
			in:   `curl -H "Cookie: a=1; session=xyz" https://x`,
			want: `curl -H "Cookie: [redacted]" https://x`,
		},
		{
			name: "authorization header with another scheme",
			in:   `Authorization: Bot abc.def`,
			want: `Authorization: [redacted]`,
		},
		{
			name: "host and port with auth in the name",
			in:   `curl http://auth-service:8080/health`,
			want: `curl http://auth-service:8080/health`,
		},
		{
			name: "image tag with token in the name",
			in:   `docker pull ghcr.io/acme/token-api:1.4.2`,
			want: `docker pull ghcr.io/acme/token-api:1.4.2`,
		},
		{
			name: "author line",
			in:   `Author: Bob <bob@example.com>`,
			want: `Author: Bob <bob@example.com>`,
		},
		{
			name: "curl user and password",
			in:   `curl -u bob:hunter2 https://x`,
			want: `curl -u bob:[redacted] https://x`,
		},
		{
			name: "curl long user flag quoted",
			in:   `curl --user "bob:hunter 2" https://x`,
			want: `curl --user "bob:[redacted]" https://x`,
		},
		{
			name: "curl api key as user with empty password",
			in:   `curl -u sk_live_123: https://x`,
			want: `curl -u [redacted]: https://x`,
		},
		{
			name: "docker user and group",
			in:   `docker run -u 1000:1000 img`,
			want: `docker run -u 1000:1000 img`,
		},
		{
			name: "date in UTC with a time format",
			in:   `date -u +%H:%M`,
			want: `date -u +%H:%M`,
		},
		{
			name: "short u flag without password",
			in:   `git push -u origin main`,
			want: `git push -u origin main`,
		},
		{
			name: "url query key and signature",
			in:   `curl "https://x/maps?key=AIza123&q=1&sig=abc&signature=def"`,
			want: `curl "https://x/maps?key=[redacted]&q=1&sig=[redacted]&signature=[redacted]"`,
		},
		{
			name: "nested JSON secret",
			in:   `{"env":{"API_TOKEN":"x"}}`,
			want: `{"env":{"API_TOKEN":"[redacted]"}}`,
		},
		{
			name: "JSON array of objects",
			in:   `[{"password":"p"}]`,
			want: `[{"password":"[redacted]"}]`,
		},
		{
			name: "multi-line JSON",
			in:   "{\n  \"command\": \"ls\",\n  \"token\": \"x\"\n}",
			want: "{\n  \"command\": \"ls\",\n  \"token\": \"[redacted]\"\n}",
		},
		{
			name: "JSON number under secret key",
			in:   `{"password":123456}`,
			want: `{"password":"[redacted]"}`,
		},
		{
			name: "JSON object under secret key",
			in:   `{"credentials":{"user":"bob","pass":"hunter2"}}`,
			want: `{"credentials":"[redacted]"}`,
		},
		{
			name: "JSON array under secret key",
			in:   `{"tokens":["ghp_x"]}`,
			want: `{"tokens":"[redacted]"}`,
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
