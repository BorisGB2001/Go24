// Package auth implements login through Authentik (OpenID Connect) and signed session cookies.
package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Claims are the ID-token fields go24 uses. Authentik sends "groups" with the default "profile" scope.
type Claims struct {
	Sub      string   `json:"sub"`
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Username string   `json:"preferred_username"`
	Groups   []string `json:"groups"`
}

// OIDC discovers the provider lazily, so go24 starts even while Authentik is still booting.
type OIDC struct {
	issuer, clientID, clientSecret, redirectURL string

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

func NewOIDC(issuer, clientID, clientSecret, redirectURL string) *OIDC {
	return &OIDC{issuer: issuer, clientID: clientID, clientSecret: clientSecret, redirectURL: redirectURL}
}

func (o *OIDC) init(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.oauth != nil {
		return nil
	}
	provider, err := oidc.NewProvider(ctx, o.issuer)
	if err != nil {
		return fmt.Errorf("oidc discovery %s: %w", o.issuer, err)
	}
	o.verifier = provider.Verifier(&oidc.Config{ClientID: o.clientID})
	o.oauth = &oauth2.Config{
		ClientID:     o.clientID,
		ClientSecret: o.clientSecret,
		RedirectURL:  o.redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	return nil
}

func (o *OIDC) AuthCodeURL(ctx context.Context, state string) (string, error) {
	if err := o.init(ctx); err != nil {
		return "", err
	}
	return o.oauth.AuthCodeURL(state), nil
}

func (o *OIDC) Exchange(ctx context.Context, code string) (Claims, error) {
	var c Claims
	if err := o.init(ctx); err != nil {
		return c, err
	}
	tok, err := o.oauth.Exchange(ctx, code)
	if err != nil {
		return c, fmt.Errorf("token exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return c, errors.New("no id_token in token response")
	}
	idt, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		return c, fmt.Errorf("verify id_token: %w", err)
	}
	if err := idt.Claims(&c); err != nil {
		return c, err
	}
	if c.Name == "" {
		c.Name = c.Username
	}
	return c, nil
}
