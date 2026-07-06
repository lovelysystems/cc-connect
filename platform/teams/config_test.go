package teams

import "testing"

func validOpts() map[string]any {
	return map[string]any{
		"app_id":       "app-123",
		"app_password": "secret",
		"tenant_id":    "tenant-abc",
	}
}

func TestParseConfig_Valid(t *testing.T) {
	c, err := parseConfig(validOpts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.appID != "app-123" || c.appPassword != "secret" {
		t.Fatalf("app credentials not parsed: %+v", c)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	c, err := parseConfig(validOpts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.webhookPort != defaultWebhookPort {
		t.Errorf("webhookPort = %q, want %q", c.webhookPort, defaultWebhookPort)
	}
	if c.webhookPath != defaultWebhookPath {
		t.Errorf("webhookPath = %q, want %q", c.webhookPath, defaultWebhookPath)
	}
	if c.sessionScope != "thread" {
		t.Errorf("sessionScope = %q, want thread", c.sessionScope)
	}
}

func TestParseConfig_MissingAppID(t *testing.T) {
	opts := validOpts()
	delete(opts, "app_id")
	if _, err := parseConfig(opts); err == nil {
		t.Fatal("expected error for missing app_id")
	}
}

func TestParseConfig_MissingAppPassword(t *testing.T) {
	opts := validOpts()
	delete(opts, "app_password")
	if _, err := parseConfig(opts); err == nil {
		t.Fatal("expected error for missing app_password")
	}
}

func TestParseConfig_MissingTenantID(t *testing.T) {
	opts := validOpts()
	delete(opts, "tenant_id")
	if _, err := parseConfig(opts); err == nil {
		t.Fatal("expected error for missing tenant_id (connector is single-tenant only)")
	}
}

func TestParseConfig_WebhookPathNormalized(t *testing.T) {
	opts := validOpts()
	opts["webhook_path"] = "teams/hook"
	c, err := parseConfig(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.webhookPath != "/teams/hook" {
		t.Errorf("webhookPath = %q, want /teams/hook", c.webhookPath)
	}
}

func TestParseConfig_SessionScope(t *testing.T) {
	cases := map[string]string{
		"":        "thread",
		"thread":  "thread",
		"channel": "channel",
		"user":    "user",
		"bogus":   "thread",
	}
	for in, want := range cases {
		opts := validOpts()
		opts["session_scope"] = in
		c, err := parseConfig(opts)
		if err != nil {
			t.Fatalf("scope %q: unexpected error: %v", in, err)
		}
		if c.sessionScope != want {
			t.Errorf("session_scope %q -> %q, want %q", in, c.sessionScope, want)
		}
	}
}

func TestParseConfig_CardUpdateIntervalMS(t *testing.T) {
	cases := map[any]int{
		int64(900): 900,
		900:        900,
		0:          defaultCardUpdateIntervalMS,
		-1:         defaultCardUpdateIntervalMS,
		"nope":     defaultCardUpdateIntervalMS,
	}
	for in, want := range cases {
		opts := validOpts()
		opts["card_update_interval_ms"] = in
		c, err := parseConfig(opts)
		if err != nil {
			t.Fatalf("interval %v: unexpected error: %v", in, err)
		}
		if c.cardUpdateIntervalMS != want {
			t.Errorf("card_update_interval_ms %v -> %d, want %d", in, c.cardUpdateIntervalMS, want)
		}
	}
	c, _ := parseConfig(validOpts())
	if c.cardUpdateIntervalMS != defaultCardUpdateIntervalMS {
		t.Errorf("absent -> %d, want default %d", c.cardUpdateIntervalMS, defaultCardUpdateIntervalMS)
	}
}

func TestNew_RegistersAsPlatform(t *testing.T) {
	p, err := New(validOpts())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != "teams" {
		t.Errorf("Name() = %q, want teams", p.Name())
	}
}
