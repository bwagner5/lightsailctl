// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package iamoidc

import (
	"context"
	"crypto/sha1" //nolint:gosec // IAM CreateOpenIDConnectProvider requires SHA-1 thumbprints.
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"
)

// Provisioner wraps the subset of IAM we need for the OIDC + role +
// inline-policy lifecycle. It does not own the iam.Client so callers
// can share a single client across enable and disable flows (and with
// their own credentials resolution).
type Provisioner struct {
	IAM *iam.Client
}

// NewIAMClient builds an iam.Client from an aws.Config. IAM is a
// global service but the SDK's endpoint resolver still requires a
// region; an empty-region Config makes every call fail with
// "Missing Region". We paper over that by forcing us-east-1 when the
// caller's Config has no region pinned.
//
// Callers that have Lightsail pinned to a region (the common case
// inside a deploy) will have that region passed through unchanged —
// IAM honors any region in the aws partition.
func NewIAMClient(cfg aws.Config) *iam.Client {
	if cfg.Region == "" {
		cfg = cfg.Copy()
		cfg.Region = "us-east-1"
	}
	return iam.NewFromConfig(cfg)
}

// EnsureOIDCProvider is idempotent: if the GitHub OIDC provider
// already exists in the account it returns its ARN with reused=true;
// otherwise it creates it. The returned error is nil on both paths
// unless the API itself fails.
//
// The thumbprint list is populated with a live SHA-1 fingerprint of
// token.actions.githubusercontent.com's current leaf cert. AWS accepts
// any non-empty list here — it ignores the thumbprints for the GitHub
// issuer because the issuer is on their IAM allow-list — but the field
// is still required by the API. Using a live fingerprint keeps us safe
// if that ever changes.
func (p *Provisioner) EnsureOIDCProvider(ctx context.Context, accountID string) (arn string, reused bool, err error) {
	wanted := OIDCProviderARN(accountID)

	// Cheapest idempotent check: direct GetOpenIDConnectProvider on
	// the canonical ARN. If it exists we're done.
	if _, gerr := p.IAM.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(wanted),
	}); gerr == nil {
		return wanted, true, nil
	} else if !isIAMNotFound(gerr) {
		return "", false, fmt.Errorf("get oidc provider: %w", gerr)
	}

	thumbprint, terr := fetchIssuerThumbprint(ctx, GitHubIssuerHost)
	if terr != nil {
		// Non-fatal: AWS accepts any non-empty thumbprint for the
		// GitHub issuer. Fall through with a well-known placeholder
		// rather than failing the whole flow on DNS/TLS weirdness.
		thumbprint = "6938fd4d98bab03faadb97b34396831e3780aea1"
	}

	_, cerr := p.IAM.CreateOpenIDConnectProvider(ctx, &iam.CreateOpenIDConnectProviderInput{
		Url:            aws.String(GitHubIssuerURL),
		ClientIDList:   []string{STSAudience},
		ThumbprintList: []string{thumbprint},
	})
	if cerr != nil {
		if isIAMEntityExists(cerr) {
			// Race: someone created it between our Get and our Create.
			return wanted, true, nil
		}
		return "", false, fmt.Errorf("create oidc provider: %w", cerr)
	}
	return wanted, false, nil
}

// fetchIssuerThumbprint dials the OIDC issuer on 443 and returns the
// SHA-1 thumbprint of the leaf certificate, in the lowercase-hex form
// IAM expects. Best-effort; caller should tolerate errors (see
// EnsureOIDCProvider).
func fetchIssuerThumbprint(ctx context.Context, host string) (string, error) {
	d := &tls.Dialer{
		Config: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: host,
		},
	}
	conn, err := d.DialContext(ctx, "tcp", host+":443")
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return "", errors.New("not a TLS connection")
	}
	chains := tc.ConnectionState().PeerCertificates
	if len(chains) == 0 {
		return "", errors.New("no peer certificates")
	}
	leaf := chains[0]
	sum := sha1.Sum(leaf.Raw) //nolint:gosec // IAM thumbprint format.
	return hex.EncodeToString(sum[:]), nil
}

// isIAMNotFound reports whether err is an IAM NoSuchEntity / EntityNotFound.
func isIAMNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchEntity", "NoSuchEntityException":
			return true
		}
	}
	// Types-level match for the IAM-specific shape.
	var nse *iamtypes.NoSuchEntityException
	return errors.As(err, &nse)
}

// isIAMEntityExists reports whether err is an IAM EntityAlreadyExists.
// Used to make Create* calls idempotent.
func isIAMEntityExists(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() == "EntityAlreadyExists" ||
			apiErr.ErrorCode() == "EntityAlreadyExistsException" {
			return true
		}
	}
	var eae *iamtypes.EntityAlreadyExistsException
	return errors.As(err, &eae)
}
