package teams

import (
	"context"
	"fmt"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Bot Framework inbound-token constants. Mirrors the Microsoft 365 Agents SDK
// (jwt_token_validator.py / agent_auth_configuration.py): the channel signs
// activity tokens as the Bot Framework issuer, validated against the ABS JWKS.
const (
	issuerBotFramework  = "https://api.botframework.com"
	jwksBotFrameworkURL = "https://login.botframework.com/v1/.well-known/keys"
	defaultLeeway       = 5 * time.Minute
)

func aadJWKSURL(tenant string) string {
	return fmt.Sprintf("https://login.microsoftonline.com/%s/discovery/v2.0/keys", tenant)
}

// aadIssuers returns the AAD issuer strings accepted for a tenant, matching the
// SDK's ISSUERS list (sts.windows.net v1 and login.microsoftonline.com v2).
func aadIssuers(tenant string) []string {
	return []string{
		fmt.Sprintf("https://sts.windows.net/%s/", tenant),
		fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", tenant),
	}
}

// inboundValidator verifies Bot Framework activity JWTs. It selects the signing
// keys by issuer (Bot Framework vs the configured AAD tenant) and enforces
// RS256, audience == app ID, issuer trust, and expiry with leeway.
type inboundValidator struct {
	appID    string
	tenantID string
	leeway   time.Duration

	bfKeys  jwt.Keyfunc // Bot Framework JWKS
	aadKeys jwt.Keyfunc // AAD tenant JWKS (nil when no tenant configured)
}

// newInboundValidator wires JWKS-backed keyfuncs with rotation caching. The AAD
// keyfunc is only created when a tenant is configured.
func newInboundValidator(cfg config) (*inboundValidator, error) {
	bf, err := keyfunc.NewDefaultCtx(context.Background(), []string{jwksBotFrameworkURL})
	if err != nil {
		return nil, fmt.Errorf("teams: bot framework JWKS: %w", err)
	}
	v := &inboundValidator{
		appID:    cfg.appID,
		tenantID: cfg.tenantID,
		leeway:   defaultLeeway,
		bfKeys:   bf.Keyfunc,
	}
	if cfg.tenantID != "" {
		aad, err := keyfunc.NewDefaultCtx(context.Background(), []string{aadJWKSURL(cfg.tenantID)})
		if err != nil {
			return nil, fmt.Errorf("teams: AAD JWKS: %w", err)
		}
		v.aadKeys = aad.Keyfunc
	}
	return v, nil
}

// keyFor selects the verification key by the token's issuer, rejecting any
// issuer the connector does not trust before a signature check is attempted.
func (v *inboundValidator) keyFor(token *jwt.Token) (any, error) {
	iss, err := token.Claims.GetIssuer()
	if err != nil {
		return nil, fmt.Errorf("teams: token missing issuer: %w", err)
	}
	if iss == issuerBotFramework {
		return v.bfKeys(token)
	}
	if v.aadKeys != nil {
		for _, trusted := range aadIssuers(v.tenantID) {
			if iss == trusted {
				return v.aadKeys(token)
			}
		}
	}
	return nil, fmt.Errorf("teams: untrusted issuer %q", iss)
}

// validate verifies a raw bearer token and returns its claims, or an error if
// the token fails any check.
func (v *inboundValidator) validate(tokenString string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, v.keyFor,
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithLeeway(v.leeway),
		jwt.WithAudience(v.appID),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("teams: token validation failed: %w", err)
	}
	return claims, nil
}
