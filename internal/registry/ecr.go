package registry

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/google/go-containerregistry/pkg/authn"
)

// ecrHostPattern matches private Amazon ECR registry hosts, including the FIPS
// endpoints and the China and GovCloud partitions:
//
//	123456789012.dkr.ecr.eu-west-1.amazonaws.com
//	123456789012.dkr.ecr-fips.us-gov-west-1.amazonaws.com
//	123456789012.dkr.ecr.cn-north-1.amazonaws.com.cn
//
// Public ECR (public.ecr.aws) is a different service with a different token
// API and is not matched here.
var ecrHostPattern = regexp.MustCompile(`^([0-9]{12})\.dkr\.ecr(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?$`)

// ParseECRHost reports whether host is a private Amazon ECR registry and, if
// so, returns the AWS account that owns it and the region it lives in.
func ParseECRHost(host string) (registryID, region string, ok bool) {
	m := ecrHostPattern.FindStringSubmatch(strings.ToLower(host))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ecrToken is an ECR authorization token with the time it stops being valid.
type ecrToken struct {
	auth      authn.AuthConfig
	expiresAt time.Time
}

// ecrTokenFunc fetches an authorization token for one ECR registry. It is a
// field on ECRKeychain so tests can supply tokens without calling AWS.
type ecrTokenFunc func(ctx context.Context, region, registryID string) (ecrToken, error)

// ECRKeychain authenticates to private Amazon ECR registries with the AWS
// credential chain (environment variables, shared config, IRSA, instance
// role), so a workflow that has AWS credentials does not additionally need a
// `docker login` against ECR.
//
// This matters for the GitHub Action in particular: it runs as a Docker
// container action, and a container action cannot read the runner's
// ~/.docker/config.json, so credentials written by aws-actions/amazon-ecr-login
// on the host are invisible to it.
//
// Docker credential config still wins when it has an entry for the registry:
// an explicit login is an explicit instruction, and honouring it keeps
// existing setups working unchanged.
type ECRKeychain struct {
	fallback authn.Keychain
	tokenFn  ecrTokenFunc

	mu     sync.Mutex
	tokens map[string]ecrToken
}

// ecrTokenRefreshWindow is how long before expiry a cached token is discarded.
// ECR tokens are valid for 12 hours, so this is generous.
const ecrTokenRefreshWindow = 5 * time.Minute

// ecrTokenTimeout bounds a single GetAuthorizationToken call. authn.Keychain
// has no context parameter, so the deadline is applied here.
const ecrTokenTimeout = 30 * time.Second

// NewECRKeychain returns a keychain that resolves private ECR hosts through
// the AWS API and delegates everything else to fallback. A nil fallback means
// authn.DefaultKeychain.
func NewECRKeychain(fallback authn.Keychain) *ECRKeychain {
	if fallback == nil {
		fallback = authn.DefaultKeychain
	}
	return &ECRKeychain{
		fallback: fallback,
		tokenFn:  fetchECRToken,
		tokens:   make(map[string]ecrToken),
	}
}

// DefaultDestinationKeychain is the keychain used for destination registries:
// docker credential config, plus native AWS authentication for private ECR.
func DefaultDestinationKeychain() authn.Keychain {
	return NewECRKeychain(authn.DefaultKeychain)
}

func (k *ECRKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	registryID, region, ok := ParseECRHost(resource.RegistryStr())
	if !ok {
		return k.fallback.Resolve(resource)
	}

	// An explicit docker login for this registry takes precedence.
	if auth, err := k.fallback.Resolve(resource); err == nil && auth != authn.Anonymous {
		return auth, nil
	}

	tok, err := k.token(region, registryID)
	if err != nil {
		return nil, err
	}
	return authn.FromConfig(tok.auth), nil
}

// token returns a cached token for the registry, fetching a new one when the
// cache is empty or the cached token is close to expiring.
func (k *ECRKeychain) token(region, registryID string) (ecrToken, error) {
	key := region + "/" + registryID

	k.mu.Lock()
	defer k.mu.Unlock()

	if tok, ok := k.tokens[key]; ok && time.Until(tok.expiresAt) > ecrTokenRefreshWindow {
		return tok, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ecrTokenTimeout)
	defer cancel()

	tok, err := k.tokenFn(ctx, region, registryID)
	if err != nil {
		return ecrToken{}, fmt.Errorf("ecr: authenticate to %s.dkr.ecr.%s.amazonaws.com: %w", registryID, region, err)
	}

	if k.tokens == nil {
		k.tokens = make(map[string]ecrToken)
	}
	k.tokens[key] = tok
	return tok, nil
}

// fetchECRToken calls ecr:GetAuthorizationToken and decodes the returned
// "user:password" pair.
func fetchECRToken(ctx context.Context, region, registryID string) (ecrToken, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return ecrToken{}, fmt.Errorf("load AWS configuration: %w", err)
	}

	out, err := ecr.NewFromConfig(cfg).GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return ecrToken{}, fmt.Errorf("get authorization token: %w", err)
	}
	if len(out.AuthorizationData) == 0 {
		return ecrToken{}, fmt.Errorf("get authorization token: response contained no authorization data")
	}

	data := out.AuthorizationData[0]
	if data.AuthorizationToken == nil {
		return ecrToken{}, fmt.Errorf("get authorization token: response contained no token")
	}

	auth, err := decodeECRToken(*data.AuthorizationToken)
	if err != nil {
		return ecrToken{}, err
	}

	tok := ecrToken{auth: auth}
	if data.ExpiresAt != nil {
		tok.expiresAt = *data.ExpiresAt
	}
	return tok, nil
}

// decodeECRToken turns the base64 "AWS:<password>" token ECR returns into an
// authn.AuthConfig.
func decodeECRToken(token string) (authn.AuthConfig, error) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return authn.AuthConfig{}, fmt.Errorf("decode authorization token: %w", err)
	}

	user, pass, found := strings.Cut(string(raw), ":")
	if !found {
		return authn.AuthConfig{}, fmt.Errorf("decode authorization token: expected user:password")
	}
	return authn.AuthConfig{Username: user, Password: pass}, nil
}
