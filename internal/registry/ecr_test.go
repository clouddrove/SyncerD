package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

func TestParseECRHost(t *testing.T) {
	cases := []struct {
		host       string
		registryID string
		region     string
		ok         bool
	}{
		{"123456789012.dkr.ecr.eu-west-1.amazonaws.com", "123456789012", "eu-west-1", true},
		{"123456789012.dkr.ecr-fips.us-gov-west-1.amazonaws.com", "123456789012", "us-gov-west-1", true},
		{"123456789012.dkr.ecr.cn-north-1.amazonaws.com.cn", "123456789012", "cn-north-1", true},
		{"123456789012.DKR.ECR.eu-west-1.amazonaws.com", "123456789012", "eu-west-1", true},
		{"public.ecr.aws", "", "", false},
		{"ghcr.io", "", "", false},
		{"index.docker.io", "", "", false},
		// Not an account ID.
		{"notanaccount.dkr.ecr.eu-west-1.amazonaws.com", "", "", false},
		// A host that merely embeds an ECR host must not match.
		{"evil.com/123456789012.dkr.ecr.eu-west-1.amazonaws.com", "", "", false},
		{"123456789012.dkr.ecr.eu-west-1.amazonaws.com.evil.com", "", "", false},
	}

	for _, tc := range cases {
		registryID, region, ok := ParseECRHost(tc.host)
		if ok != tc.ok || registryID != tc.registryID || region != tc.region {
			t.Errorf("ParseECRHost(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.host, registryID, region, ok, tc.registryID, tc.region, tc.ok)
		}
	}
}

// stubKeychain returns a fixed authenticator for one registry host.
type stubKeychain struct {
	host string
	auth authn.Authenticator
	err  error
	// calls counts Resolve invocations.
	calls int
}

func (k *stubKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	k.calls++
	if k.err != nil {
		return nil, k.err
	}
	if resource.RegistryStr() == k.host {
		return k.auth, nil
	}
	return authn.Anonymous, nil
}

func mustResource(t *testing.T, host string) authn.Resource {
	t.Helper()
	reg, err := name.NewRegistry(host)
	if err != nil {
		t.Fatalf("NewRegistry(%q): %v", host, err)
	}
	return reg
}

func encodeToken(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func newTestECRKeychain(fallback authn.Keychain, fn ecrTokenFunc) *ECRKeychain {
	k := NewECRKeychain(fallback)
	k.tokenFn = fn
	return k
}

func TestECRKeychainDelegatesNonECRHosts(t *testing.T) {
	want := authn.FromConfig(authn.AuthConfig{Username: "u", Password: "p"})
	fallback := &stubKeychain{host: "ghcr.io", auth: want}
	k := newTestECRKeychain(fallback, func(context.Context, string, string) (ecrToken, error) {
		t.Fatal("token function called for a non-ECR host")
		return ecrToken{}, nil
	})

	got, err := k.Resolve(mustResource(t, "ghcr.io"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve returned %v, want the fallback authenticator", got)
	}
}

func TestECRKeychainUsesAWSTokenWhenDockerConfigIsEmpty(t *testing.T) {
	const host = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"

	var gotRegion, gotRegistryID string
	k := newTestECRKeychain(&stubKeychain{}, func(_ context.Context, region, registryID string) (ecrToken, error) {
		gotRegion, gotRegistryID = region, registryID
		return ecrToken{
			auth:      authn.AuthConfig{Username: "AWS", Password: "secret"},
			expiresAt: time.Now().Add(12 * time.Hour),
		}, nil
	})

	auth, err := k.Resolve(mustResource(t, host))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatalf("Authorization: %v", err)
	}
	if cfg.Username != "AWS" || cfg.Password != "secret" {
		t.Errorf("got %s/%s, want AWS/secret", cfg.Username, cfg.Password)
	}
	if gotRegion != "eu-west-1" || gotRegistryID != "123456789012" {
		t.Errorf("token fetched for %s/%s, want eu-west-1/123456789012", gotRegion, gotRegistryID)
	}
}

func TestECRKeychainPrefersDockerConfig(t *testing.T) {
	const host = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"

	want := authn.FromConfig(authn.AuthConfig{Username: "AWS", Password: "from-docker-login"})
	k := newTestECRKeychain(&stubKeychain{host: host, auth: want}, func(context.Context, string, string) (ecrToken, error) {
		t.Fatal("token function called even though docker config had credentials")
		return ecrToken{}, nil
	})

	got, err := k.Resolve(mustResource(t, host))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve returned %v, want the docker config authenticator", got)
	}
}

func TestECRKeychainFallsBackWhenDockerConfigErrors(t *testing.T) {
	const host = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"

	fallback := &stubKeychain{err: errors.New("no docker config")}
	k := newTestECRKeychain(fallback, func(context.Context, string, string) (ecrToken, error) {
		return ecrToken{
			auth:      authn.AuthConfig{Username: "AWS", Password: "secret"},
			expiresAt: time.Now().Add(12 * time.Hour),
		}, nil
	})

	auth, err := k.Resolve(mustResource(t, host))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatalf("Authorization: %v", err)
	}
	if cfg.Password != "secret" {
		t.Errorf("got password %q, want the AWS token", cfg.Password)
	}
}

func TestECRKeychainCachesTokenUntilExpiry(t *testing.T) {
	const host = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"

	calls := 0
	k := newTestECRKeychain(&stubKeychain{}, func(context.Context, string, string) (ecrToken, error) {
		calls++
		return ecrToken{
			auth:      authn.AuthConfig{Username: "AWS", Password: "secret"},
			expiresAt: time.Now().Add(12 * time.Hour),
		}, nil
	})

	for i := 0; i < 3; i++ {
		if _, err := k.Resolve(mustResource(t, host)); err != nil {
			t.Fatalf("Resolve %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("token fetched %d times, want 1", calls)
	}
}

func TestECRKeychainRefetchesExpiringToken(t *testing.T) {
	const host = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"

	calls := 0
	k := newTestECRKeychain(&stubKeychain{}, func(context.Context, string, string) (ecrToken, error) {
		calls++
		// Inside the refresh window, so the next Resolve must fetch again.
		return ecrToken{
			auth:      authn.AuthConfig{Username: "AWS", Password: "secret"},
			expiresAt: time.Now().Add(ecrTokenRefreshWindow / 2),
		}, nil
	})

	for i := 0; i < 2; i++ {
		if _, err := k.Resolve(mustResource(t, host)); err != nil {
			t.Fatalf("Resolve %d: %v", i, err)
		}
	}
	if calls != 2 {
		t.Errorf("token fetched %d times, want 2", calls)
	}
}

func TestECRKeychainCachesPerRegistry(t *testing.T) {
	calls := map[string]int{}
	k := newTestECRKeychain(&stubKeychain{}, func(_ context.Context, region, registryID string) (ecrToken, error) {
		calls[registryID+"/"+region]++
		return ecrToken{
			auth:      authn.AuthConfig{Username: "AWS", Password: "secret"},
			expiresAt: time.Now().Add(12 * time.Hour),
		}, nil
	})

	for _, host := range []string{
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com",
		"123456789012.dkr.ecr.us-east-1.amazonaws.com",
		"210987654321.dkr.ecr.eu-west-1.amazonaws.com",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com",
	} {
		if _, err := k.Resolve(mustResource(t, host)); err != nil {
			t.Fatalf("Resolve %s: %v", host, err)
		}
	}

	if len(calls) != 3 {
		t.Errorf("fetched tokens for %d registries, want 3: %v", len(calls), calls)
	}
	if got := calls["123456789012/eu-west-1"]; got != 1 {
		t.Errorf("repeated host fetched %d times, want 1", got)
	}
}

func TestECRKeychainReportsTokenErrors(t *testing.T) {
	const host = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"

	k := newTestECRKeychain(&stubKeychain{}, func(context.Context, string, string) (ecrToken, error) {
		return ecrToken{}, errors.New("AccessDeniedException")
	})

	_, err := k.Resolve(mustResource(t, host))
	if err == nil {
		t.Fatal("Resolve succeeded, want an error")
	}
	// The message has to name the registry, or an operator cannot tell which
	// destination is misconfigured.
	if !strings.Contains(err.Error(), host) || !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("error %q does not name the registry and the cause", err)
	}
}

func TestDecodeECRToken(t *testing.T) {
	auth, err := decodeECRToken(encodeToken("AWS", "pa:ss:word"))
	if err != nil {
		t.Fatalf("decodeECRToken: %v", err)
	}
	// Passwords may contain colons, so only the first separator counts.
	if auth.Username != "AWS" || auth.Password != "pa:ss:word" {
		t.Errorf("got %s/%s, want AWS/pa:ss:word", auth.Username, auth.Password)
	}

	if _, err := decodeECRToken("!!!not base64!!!"); err == nil {
		t.Error("decodeECRToken accepted invalid base64")
	}
	if _, err := decodeECRToken(base64.StdEncoding.EncodeToString([]byte("no-separator"))); err == nil {
		t.Error("decodeECRToken accepted a token without a separator")
	}
}

func TestDefaultDestinationKeychainHandlesECR(t *testing.T) {
	// The destination default must be ECR-aware; a plain DefaultKeychain would
	// resolve ECR hosts anonymously and every request would 401.
	k, ok := DefaultDestinationKeychain().(*ECRKeychain)
	if !ok {
		t.Fatalf("DefaultDestinationKeychain returned %T, want *ECRKeychain", k)
	}
	if _, _, isECR := ParseECRHost("123456789012.dkr.ecr.eu-west-1.amazonaws.com"); !isECR {
		t.Error("ECR host not recognised")
	}
}

func TestNewGenericRegistryUsesECRAwareKeychain(t *testing.T) {
	r := NewGenericRegistry("123456789012.dkr.ecr.eu-west-1.amazonaws.com")
	if _, ok := r.keychain.(*ECRKeychain); !ok {
		t.Errorf("generic registry keychain is %T, want *ECRKeychain", r.keychain)
	}
}
