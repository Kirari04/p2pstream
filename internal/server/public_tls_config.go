package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

func (a *App) CreatePublicTlsDnsCredential(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicTlsDnsCredentialRequest],
) (*connect.Response[p2pstreamv1.CreatePublicTlsDnsCredentialResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := a.validatePublicTLSDNSCredentialInput(
		req.Msg.Name,
		req.Msg.Provider,
		req.Msg.CloudflareZoneId,
		req.Msg.ApiToken,
		req.Msg.Enabled,
		nil,
		req.Msg.ApiToken != "",
	)
	if err != nil {
		return nil, err
	}
	credential, err := a.DB.CreatePublicTlsDnsCredential(ctx, db.CreatePublicTlsDnsCredentialParams{
		Name:             params.Name,
		Provider:         params.Provider,
		CloudflareZoneID: params.CloudflareZoneID,
		ApiToken:         params.ApiToken,
		Enabled:          params.Enabled,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicTlsDnsCredentialResponse{Credential: publicTLSDNSCredentialToProto(credential)}), nil
}

func (a *App) UpdatePublicTlsDnsCredential(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicTlsDnsCredentialRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicTlsDnsCredentialResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	existing, err := a.DB.GetPublicTlsDnsCredential(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	params, err := a.validatePublicTLSDNSCredentialInput(
		req.Msg.Name,
		req.Msg.Provider,
		req.Msg.CloudflareZoneId,
		req.Msg.ApiToken,
		req.Msg.Enabled,
		&existing,
		req.Msg.ApiTokenSet || req.Msg.ApiToken != "",
	)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	credential, err := a.DB.UpdatePublicTlsDnsCredential(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicTlsDnsCredentialResponse{Credential: publicTLSDNSCredentialToProto(credential)}), nil
}

func (a *App) DeletePublicTlsDnsCredential(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicTlsDnsCredentialRequest],
) (*connect.Response[p2pstreamv1.DeletePublicTlsDnsCredentialResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicTlsDnsCredential(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicTlsDnsCredentialResponse{}), nil
}

func (a *App) CreatePublicTlsCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicTlsCertificateRequest],
) (*connect.Response[p2pstreamv1.CreatePublicTlsCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, material, err := a.validatePublicTLSCertificateInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.HostnamePattern,
		req.Msg.CertPath,
		req.Msg.KeyPath,
		req.Msg.CertPem,
		req.Msg.KeyPem,
		req.Msg.Enabled,
		req.Msg.Source,
		req.Msg.AcmeChallengeType,
		req.Msg.AcmeCa,
		req.Msg.AcmeEmail,
		req.Msg.DnsCredentialId,
		req.Msg.GenerateSelfSigned,
		req.Msg.SelfSignedValidityDays,
		nil,
		false,
	)
	if err != nil {
		return nil, err
	}

	var cert db.PublicTlsCertificate
	if material.Replace {
		cert, err = a.createUploadedPublicTLSCertificate(ctx, params, material.CertPEM, material.KeyPEM)
	} else {
		cert, err = a.DB.CreatePublicTlsCertificate(ctx, publicTLSCertificateCreateParams(params))
	}
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	_, _ = a.restartTLSListenerIfActive(ctx, cert.ListenerID)
	a.queuePublicACMECertificateIssue(cert, publicACMETriggerConfigChange)
	return connect.NewResponse(&p2pstreamv1.CreatePublicTlsCertificateResponse{TlsCertificate: publicTLSCertificateToProto(cert)}), nil
}

func (a *App) UpdatePublicTlsCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicTlsCertificateRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicTlsCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	existing, err := a.DB.GetPublicTlsCertificate(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	params, material, err := a.validatePublicTLSCertificateInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.HostnamePattern,
		req.Msg.CertPath,
		req.Msg.KeyPath,
		req.Msg.CertPem,
		req.Msg.KeyPem,
		req.Msg.Enabled,
		req.Msg.Source,
		req.Msg.AcmeChallengeType,
		req.Msg.AcmeCa,
		req.Msg.AcmeEmail,
		req.Msg.DnsCredentialId,
		req.Msg.GenerateSelfSigned,
		req.Msg.SelfSignedValidityDays,
		&existing,
		true,
	)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	if params.CertPath == "" && params.KeyPath == "" && params.Source == publicTLSCertificateSourceManual {
		params.CertPath = existing.CertPath
		params.KeyPath = existing.KeyPath
	}
	if params.Source == publicTLSCertificateSourceACME && params.CertPath == "" && params.KeyPath == "" {
		params.CertPath = existing.CertPath
		params.KeyPath = existing.KeyPath
	}

	var cert db.PublicTlsCertificate
	if material.Replace {
		cert, err = a.updateUploadedPublicTLSCertificate(ctx, params, material.CertPEM, material.KeyPEM)
	} else {
		cert, err = a.DB.UpdatePublicTlsCertificate(ctx, publicTLSCertificateUpdateParams(params))
	}
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	if existing.ListenerID != cert.ListenerID {
		_, _ = a.restartTLSListenerIfActive(ctx, existing.ListenerID)
	}
	_, _ = a.restartTLSListenerIfActive(ctx, cert.ListenerID)
	a.queuePublicACMECertificateIssue(cert, publicACMETriggerConfigChange)
	return connect.NewResponse(&p2pstreamv1.UpdatePublicTlsCertificateResponse{TlsCertificate: publicTLSCertificateToProto(cert)}), nil
}

func (a *App) DeletePublicTlsCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicTlsCertificateRequest],
) (*connect.Response[p2pstreamv1.DeletePublicTlsCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	cert, err := a.DB.GetPublicTlsCertificate(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.DB.DeletePublicTlsCertificate(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	_, _ = a.restartTLSListenerIfActive(ctx, cert.ListenerID)
	return connect.NewResponse(&p2pstreamv1.DeletePublicTlsCertificateResponse{}), nil
}

func (a *App) RenewPublicTlsCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.RenewPublicTlsCertificateRequest],
) (*connect.Response[p2pstreamv1.RenewPublicTlsCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	cert, err := a.DB.GetPublicTlsCertificate(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if normalizePublicTLSCertificateSource(cert.Source) != publicTLSCertificateSourceACME {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("only ACME certificates can be renewed"))
	}
	cert, err = a.DB.UpdatePublicTlsCertificateRenewalStatus(ctx, db.UpdatePublicTlsCertificateRenewalStatusParams{
		ID:                   cert.ID,
		Status:               publicTLSCertificateStatusRenewing,
		LastError:            "",
		NextRenewalAt:        sql.NullTime{},
		LastRenewalAttemptAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	a.queuePublicACMECertificateIssue(cert, publicACMETriggerManual)
	return connect.NewResponse(&p2pstreamv1.RenewPublicTlsCertificateResponse{TlsCertificate: publicTLSCertificateToProto(cert)}), nil
}

func (a *App) createUploadedPublicTLSCertificate(
	ctx context.Context,
	params publicTLSCertificateMutationInput,
	certPEM []byte,
	keyPEM []byte,
) (db.PublicTlsCertificate, error) {
	params = publicTLSCertificateInputWithPEMValidity(params, certPEM)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	cert, err := qtx.CreatePublicTlsCertificate(ctx, db.CreatePublicTlsCertificateParams{
		ListenerID:           params.ListenerID,
		HostnamePattern:      params.HostnamePattern,
		CertPath:             "",
		KeyPath:              "",
		Enabled:              params.Enabled,
		Source:               publicTLSCertificateSourceManual,
		AcmeChallengeType:    "",
		AcmeCa:               "",
		AcmeEmail:            "",
		DnsCredentialID:      sql.NullInt64{},
		Status:               publicTLSCertificateStatusReady,
		LastError:            "",
		IssuedAt:             sql.NullTime{},
		ExpiresAt:            sql.NullTime{},
		NextRenewalAt:        sql.NullTime{},
		LastRenewalAttemptAt: sql.NullTime{},
	})
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}

	certPath, keyPath, err := a.writePublicTLSCertificateFiles(params.ListenerID, cert.ID, certPEM, keyPEM)
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			_ = os.Remove(certPath)
			_ = os.Remove(keyPath)
		}
	}()

	params.ID = cert.ID
	params.CertPath = certPath
	params.KeyPath = keyPath
	cert, err = qtx.UpdatePublicTlsCertificate(ctx, publicTLSCertificateUpdateParams(params))
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}
	if err := tx.Commit(); err != nil {
		return db.PublicTlsCertificate{}, err
	}
	cleanupOnError = false
	return cert, nil
}

func (a *App) updateUploadedPublicTLSCertificate(
	ctx context.Context,
	params publicTLSCertificateMutationInput,
	certPEM []byte,
	keyPEM []byte,
) (db.PublicTlsCertificate, error) {
	params = publicTLSCertificateInputWithPEMValidity(params, certPEM)
	certPath, keyPath, err := a.writePublicTLSCertificateFiles(params.ListenerID, params.ID, certPEM, keyPEM)
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}

	params.CertPath = certPath
	params.KeyPath = keyPath
	cert, err := a.DB.UpdatePublicTlsCertificate(ctx, publicTLSCertificateUpdateParams(params))
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}
	return cert, nil
}

func (a *App) writePublicTLSCertificateFiles(listenerID, mappingID int64, certPEM, keyPEM []byte) (string, string, error) {
	cfg := a.Config
	if cfg == nil {
		cfg = &config.Config{}
	}
	return cfg.WritePublicTLSCertificateFiles(listenerID, mappingID, certPEM, keyPEM)
}

func (a *App) validatePublicTLSCertificateInput(
	ctx context.Context,
	listenerID int64,
	hostnamePattern string,
	certPath string,
	keyPath string,
	certPEM []byte,
	keyPEM []byte,
	enabled bool,
	sourceProto p2pstreamv1.PublicTlsCertificateSource,
	challengeProto p2pstreamv1.PublicAcmeChallengeType,
	caProto p2pstreamv1.PublicAcmeCa,
	acmeEmail string,
	dnsCredentialID int64,
	generateSelfSigned bool,
	selfSignedValidityDays int64,
	existing *db.PublicTlsCertificate,
	allowMissingMaterial bool,
) (publicTLSCertificateMutationInput, publicTLSCertificateMaterial, error) {
	listener, err := a.DB.GetPublicListener(ctx, listenerID)
	if err != nil {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, publicDBError(err)
	}
	if listener.Protocol != publicListenerProtocolHTTPS {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("TLS certificates can only be configured on HTTPS listeners"))
	}
	hostnamePattern = normalizeHostPattern(hostnamePattern)
	if err := validateHostPattern(hostnamePattern); err != nil {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
	}

	source, err := tlsCertificateSourceStringFromProto(sourceProto)
	if err != nil {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
	}
	if sourceProto == p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED && existing != nil {
		source = normalizePublicTLSCertificateSource(existing.Source)
	}

	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)
	hasCertUpload := len(certPEM) > 0
	hasKeyUpload := len(keyPEM) > 0

	if source == publicTLSCertificateSourceACME {
		if generateSelfSigned {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("self-signed generation is only available for manual certificates"))
		}
		if hasCertUpload || hasKeyUpload || certPath != "" || keyPath != "" {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("ACME certificates manage certificate material automatically"))
		}
		challengeType, err := acmeChallengeTypeStringFromProto(challengeProto)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
		}
		ca, err := acmeCAStringFromProto(caProto)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
		}
		acmeEmail = strings.TrimSpace(strings.ToLower(acmeEmail))
		if _, err := mail.ParseAddress(acmeEmail); err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("ACME email must be a valid email address"))
		}
		if err := validateACMEHostPattern(hostnamePattern, challengeType); err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
		}
		var credentialID sql.NullInt64
		if challengeType == publicACMEChallengeDNS01 {
			if dnsCredentialID <= 0 {
				return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("DNS-01 requires a DNS credential"))
			}
			credential, err := a.DB.GetPublicTlsDnsCredential(ctx, dnsCredentialID)
			if err != nil {
				return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, publicDBError(err)
			}
			if credential.Enabled == 0 {
				return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("DNS-01 requires an enabled DNS credential"))
			}
			if normalizePublicDNSProvider(credential.Provider) != publicDNSProviderCloudflare {
				return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("DNS-01 currently supports Cloudflare credentials only"))
			}
			credentialID = sql.NullInt64{Int64: dnsCredentialID, Valid: true}
		}
		status := publicTLSCertificateStatusPending
		if existing != nil && existing.CertPath != "" && existing.KeyPath != "" && normalizePublicTLSCertificateStatus(existing.Status) == publicTLSCertificateStatusReady {
			status = publicTLSCertificateStatusReady
		}
		return publicTLSCertificateMutationInput{
			ListenerID:           listenerID,
			HostnamePattern:      hostnamePattern,
			Enabled:              boolInt(enabled),
			Source:               publicTLSCertificateSourceACME,
			ACMEChallengeType:    challengeType,
			ACMECA:               ca,
			ACMEEmail:            acmeEmail,
			DNSCredentialID:      credentialID,
			Status:               status,
			LastError:            "",
			IssuedAt:             nullTimeFromExisting(existing, "issued_at"),
			ExpiresAt:            nullTimeFromExisting(existing, "expires_at"),
			NextRenewalAt:        nullTimeFromExisting(existing, "next_renewal_at"),
			LastRenewalAttemptAt: nullTimeFromExisting(existing, "last_renewal_attempt_at"),
		}, publicTLSCertificateMaterial{}, nil
	}

	if generateSelfSigned {
		if hasCertUpload || hasKeyUpload || certPath != "" || keyPath != "" {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("self-signed generation cannot be combined with certificate uploads or file paths"))
		}
		validityDays, err := validatePublicSelfSignedValidityDays(selfSignedValidityDays)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
		}
		certPEM, keyPEM, leaf, err := generatePublicSelfSignedCertificatePEM(hostnamePattern, time.Duration(validityDays)*24*time.Hour)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInternal, err)
		}
		return publicTLSCertificateMutationInput{
			ListenerID:      listenerID,
			HostnamePattern: hostnamePattern,
			Enabled:         boolInt(enabled),
			Source:          publicTLSCertificateSourceManual,
			Status:          publicTLSCertificateStatusReady,
			IssuedAt:        sql.NullTime{Time: leaf.NotBefore.UTC(), Valid: true},
			ExpiresAt:       sql.NullTime{Time: leaf.NotAfter.UTC(), Valid: true},
		}, publicTLSCertificateMaterial{Replace: true, CertPEM: certPEM, KeyPEM: keyPEM}, nil
	}
	if selfSignedValidityDays != 0 {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("self-signed validity days require self-signed generation"))
	}

	if hasCertUpload || hasKeyUpload {
		if !hasCertUpload || !hasKeyUpload {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("certificate and private key uploads are both required"))
		}
		if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("certificate and private key must be a valid PEM pair: %w", err))
		}
		issuedAt, expiresAt, err := publicTLSCertificateValidityFromPEM(certPEM)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return publicTLSCertificateMutationInput{
			ListenerID:      listenerID,
			HostnamePattern: hostnamePattern,
			Enabled:         boolInt(enabled),
			Source:          publicTLSCertificateSourceManual,
			Status:          publicTLSCertificateStatusReady,
			IssuedAt:        issuedAt,
			ExpiresAt:       expiresAt,
		}, publicTLSCertificateMaterial{Replace: true, CertPEM: certPEM, KeyPEM: keyPEM}, nil
	}

	if certPath == "" && keyPath == "" && allowMissingMaterial {
		if existing == nil || normalizePublicTLSCertificateSource(existing.Source) != publicTLSCertificateSourceManual {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("manual certificates require uploaded files, server file paths, or self-signed generation"))
		}
		return publicTLSCertificateMutationInput{
			ListenerID:           listenerID,
			HostnamePattern:      hostnamePattern,
			Enabled:              boolInt(enabled),
			Source:               publicTLSCertificateSourceManual,
			Status:               publicTLSCertificateStatusReady,
			IssuedAt:             nullTimeFromExisting(existing, "issued_at"),
			ExpiresAt:            nullTimeFromExisting(existing, "expires_at"),
			NextRenewalAt:        nullTimeFromExisting(existing, "next_renewal_at"),
			LastRenewalAttemptAt: nullTimeFromExisting(existing, "last_renewal_attempt_at"),
		}, publicTLSCertificateMaterial{}, nil
	}
	if certPath == "" || keyPath == "" {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("certificate and key paths are required"))
	}
	issuedAt, expiresAt := publicTLSCertificateValidityFromFile(certPath)
	return publicTLSCertificateMutationInput{
		ListenerID:      listenerID,
		HostnamePattern: hostnamePattern,
		CertPath:        certPath,
		KeyPath:         keyPath,
		Enabled:         boolInt(enabled),
		Source:          publicTLSCertificateSourceManual,
		Status:          publicTLSCertificateStatusReady,
		IssuedAt:        issuedAt,
		ExpiresAt:       expiresAt,
	}, publicTLSCertificateMaterial{}, nil
}

func (a *App) validatePublicTLSDNSCredentialInput(
	name string,
	providerProto p2pstreamv1.PublicDnsProvider,
	cloudflareZoneID string,
	apiToken string,
	enabled bool,
	existing *db.PublicTlsDnsCredential,
	replaceToken bool,
) (db.UpdatePublicTlsDnsCredentialParams, error) {
	name, err := normalizePublicName(name)
	if err != nil {
		return db.UpdatePublicTlsDnsCredentialParams{}, err
	}
	provider, err := dnsProviderStringFromProto(providerProto)
	if err != nil {
		return db.UpdatePublicTlsDnsCredentialParams{}, err
	}
	cloudflareZoneID = strings.TrimSpace(cloudflareZoneID)
	if cloudflareZoneID == "" || strings.ContainsAny(cloudflareZoneID, " /\r\n\t") {
		return db.UpdatePublicTlsDnsCredentialParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("Cloudflare zone ID is required"))
	}
	apiToken = strings.TrimSpace(apiToken)
	if replaceToken {
		if apiToken == "" {
			return db.UpdatePublicTlsDnsCredentialParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("Cloudflare API token is required"))
		}
		if strings.ContainsAny(apiToken, "\r\n") || !utf8.ValidString(apiToken) {
			return db.UpdatePublicTlsDnsCredentialParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("Cloudflare API token must be valid UTF-8 without CR or LF"))
		}
	} else if existing != nil {
		apiToken = existing.ApiToken
	} else {
		return db.UpdatePublicTlsDnsCredentialParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("Cloudflare API token is required"))
	}
	return db.UpdatePublicTlsDnsCredentialParams{
		Name:             name,
		Provider:         provider,
		CloudflareZoneID: cloudflareZoneID,
		ApiToken:         apiToken,
		Enabled:          boolInt(enabled),
	}, nil
}

func publicTLSCertificateCreateParams(input publicTLSCertificateMutationInput) db.CreatePublicTlsCertificateParams {
	return db.CreatePublicTlsCertificateParams{
		ListenerID:           input.ListenerID,
		HostnamePattern:      input.HostnamePattern,
		CertPath:             input.CertPath,
		KeyPath:              input.KeyPath,
		Enabled:              input.Enabled,
		Source:               input.Source,
		AcmeChallengeType:    input.ACMEChallengeType,
		AcmeCa:               input.ACMECA,
		AcmeEmail:            input.ACMEEmail,
		DnsCredentialID:      input.DNSCredentialID,
		Status:               input.Status,
		LastError:            input.LastError,
		IssuedAt:             input.IssuedAt,
		ExpiresAt:            input.ExpiresAt,
		NextRenewalAt:        input.NextRenewalAt,
		LastRenewalAttemptAt: input.LastRenewalAttemptAt,
	}
}

func publicTLSCertificateUpdateParams(input publicTLSCertificateMutationInput) db.UpdatePublicTlsCertificateParams {
	return db.UpdatePublicTlsCertificateParams{
		ID:                   input.ID,
		ListenerID:           input.ListenerID,
		HostnamePattern:      input.HostnamePattern,
		CertPath:             input.CertPath,
		KeyPath:              input.KeyPath,
		Enabled:              input.Enabled,
		Source:               input.Source,
		AcmeChallengeType:    input.ACMEChallengeType,
		AcmeCa:               input.ACMECA,
		AcmeEmail:            input.ACMEEmail,
		DnsCredentialID:      input.DNSCredentialID,
		Status:               input.Status,
		LastError:            input.LastError,
		IssuedAt:             input.IssuedAt,
		ExpiresAt:            input.ExpiresAt,
		NextRenewalAt:        input.NextRenewalAt,
		LastRenewalAttemptAt: input.LastRenewalAttemptAt,
	}
}

func nullTimeFromExisting(existing *db.PublicTlsCertificate, field string) sql.NullTime {
	if existing == nil {
		return sql.NullTime{}
	}
	switch field {
	case "issued_at":
		return existing.IssuedAt
	case "expires_at":
		return existing.ExpiresAt
	case "next_renewal_at":
		return existing.NextRenewalAt
	case "last_renewal_attempt_at":
		return existing.LastRenewalAttemptAt
	default:
		return sql.NullTime{}
	}
}

func validatePublicSelfSignedValidityDays(days int64) (int64, error) {
	if days < minPublicSelfSignedValidityDays || days > maxPublicSelfSignedValidityDays {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("self-signed certificate validity must be between %d and %d days", minPublicSelfSignedValidityDays, maxPublicSelfSignedValidityDays))
	}
	return days, nil
}

func publicTLSCertificateInputWithPEMValidity(input publicTLSCertificateMutationInput, certPEM []byte) publicTLSCertificateMutationInput {
	if input.IssuedAt.Valid && input.ExpiresAt.Valid {
		return input
	}
	issuedAt, expiresAt, err := publicTLSCertificateValidityFromPEM(certPEM)
	if err != nil {
		return input
	}
	if !input.IssuedAt.Valid {
		input.IssuedAt = issuedAt
	}
	if !input.ExpiresAt.Valid {
		input.ExpiresAt = expiresAt
	}
	return input
}

func publicTLSCertificateValidityFromPEM(certPEM []byte) (sql.NullTime, sql.NullTime, error) {
	leaf, err := parseLeafCertificate(certPEM)
	if err != nil {
		return sql.NullTime{}, sql.NullTime{}, fmt.Errorf("certificate PEM must contain a valid leaf certificate: %w", err)
	}
	return sql.NullTime{Time: leaf.NotBefore.UTC(), Valid: true}, sql.NullTime{Time: leaf.NotAfter.UTC(), Valid: true}, nil
}

func publicTLSCertificateValidityFromFile(certPath string) (sql.NullTime, sql.NullTime) {
	certPEM, err := os.ReadFile(strings.TrimSpace(certPath))
	if err != nil {
		return sql.NullTime{}, sql.NullTime{}
	}
	issuedAt, expiresAt, err := publicTLSCertificateValidityFromPEM(certPEM)
	if err != nil {
		return sql.NullTime{}, sql.NullTime{}
	}
	return issuedAt, expiresAt
}

func validateACMEHostPattern(pattern string, challengeType string) error {
	if pattern == defaultSelfSignedTLSHost || pattern == "localhost" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("ACME certificates require a public DNS hostname"))
	}
	host := strings.TrimPrefix(pattern, "*.")
	if net.ParseIP(host) != nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("ACME certificates require a DNS hostname, not an IP address"))
	}
	if !strings.Contains(host, ".") {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("ACME certificates require a fully-qualified DNS hostname"))
	}
	if strings.HasPrefix(pattern, "*.") && challengeType != publicACMEChallengeDNS01 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("wildcard ACME certificates require DNS-01"))
	}
	return nil
}

func (a *App) queuePublicACMECertificateIssue(cert db.PublicTlsCertificate, trigger string) {
	if normalizePublicTLSCertificateSource(cert.Source) != publicTLSCertificateSourceACME || cert.Enabled == 0 || a.PublicACME == nil {
		return
	}
	a.PublicACME.QueueCertificate(cert, trigger)
}
