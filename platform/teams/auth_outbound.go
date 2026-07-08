package teams

import (
	"context"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// connectorScope is the client-credentials scope for calling the Bot Connector
// (M365 SDK: AGENTS_SDK_SCOPE + "/.default").
const connectorScope = "https://api.botframework.com/.default"

// tokenURL is the single-tenant client-credentials endpoint. tenant is required
// (enforced by parseConfig) — the connector does not support multi-tenant bots,
// whose creation Azure deprecated after 2025-07-31.
func tokenURL(tenant string) string {
	return fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant)
}

// tokenSource yields cached, auto-refreshed Bot Connector access tokens.
type tokenSource interface {
	token(ctx context.Context) (string, error)
}

type oauthTokenSource struct {
	src oauth2.TokenSource
}

// newTokenSource builds a client-credentials token source for the Bot Connector.
func newTokenSource(cfg config) *oauthTokenSource {
	return newTokenSourceWithURL(cfg, tokenURL(cfg.tenantID))
}

// newTokenSourceWithURL allows overriding the token endpoint (tests).
func newTokenSourceWithURL(cfg config, url string) *oauthTokenSource {
	return newOAuthSource(cfg, url, connectorScope)
}

// newOAuthSource builds a cached client-credentials token source for the given
// resource scope, reusing the app credentials. Used for both the Bot Connector
// (connectorScope) and Microsoft Graph (graphScope) resources — all scopes in a
// single request must target one resource.
func newOAuthSource(cfg config, url, scope string) *oauthTokenSource {
	conf := &clientcredentials.Config{
		ClientID:     cfg.appID,
		ClientSecret: cfg.appPassword,
		TokenURL:     url,
		Scopes:       []string{scope},
		AuthStyle:    oauth2.AuthStyleInParams,
	}
	// Bound the token fetch with a timeout-bearing HTTP client — the caller
	// acquires the token before its own http.Client (with connectorTimeout) runs,
	// so without this an unreachable login.microsoftonline.com would hang the turn
	// goroutine indefinitely. clientcredentials.Config.TokenSource caches and
	// refreshes internally.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: connectorTimeout})
	return &oauthTokenSource{src: conf.TokenSource(ctx)}
}

func (o *oauthTokenSource) token(ctx context.Context) (string, error) {
	tok, err := o.src.Token()
	if err != nil {
		return "", fmt.Errorf("teams: acquire connector token: %w", err)
	}
	return tok.AccessToken, nil
}
