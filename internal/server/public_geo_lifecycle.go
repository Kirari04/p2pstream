package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"p2pstream/internal/db"
)

const (
	publicGeoRefreshAge          = 24 * time.Hour
	publicGeoMaintenanceInterval = time.Hour
)

func (a *App) LoadPublicGeoRuntime() error {
	if a == nil || a.GeoConfigRefresher == nil {
		return ErrGeoIPCountryDatabaseUnavailable
	}
	loader, ok := a.GeoConfigRefresher.(interface{ Load() error })
	if !ok {
		return errors.New("GeoIP runtime does not support loading")
	}
	return loader.Load()
}

func (a *App) ClosePublicGeoRuntime() error {
	if a == nil {
		return nil
	}
	a.StopPublicGeoMaintenance()
	if a.GeoConfigRefresher == nil {
		return nil
	}
	closer, ok := a.GeoConfigRefresher.(interface{ Close() error })
	if !ok {
		return nil
	}
	return closer.Close()
}

func (a *App) StartPublicGeoMaintenance(ctx context.Context) {
	if a == nil || a.DB == nil || a.GeoConfigRefresher == nil {
		return
	}
	a.publicGeoMaintenanceMu.Lock()
	if a.publicGeoMaintenanceStarted {
		a.publicGeoMaintenanceMu.Unlock()
		return
	}
	maintenanceCtx, cancel := context.WithCancel(ctx)
	a.publicGeoMaintenanceStarted = true
	a.publicGeoMaintenanceCancel = cancel
	a.publicGeoMaintenanceWG.Add(1)
	a.publicGeoMaintenanceMu.Unlock()
	go func() {
		defer a.publicGeoMaintenanceWG.Done()
		a.runPublicGeoMaintenance(maintenanceCtx, time.Now().UTC())
		ticker := time.NewTicker(publicGeoMaintenanceInterval)
		defer ticker.Stop()
		for {
			select {
			case <-maintenanceCtx.Done():
				return
			case now := <-ticker.C:
				a.runPublicGeoMaintenance(maintenanceCtx, now.UTC())
			}
		}
	}()
}

func (a *App) StopPublicGeoMaintenance() {
	if a == nil {
		return
	}
	a.publicGeoMaintenanceMu.Lock()
	cancel := a.publicGeoMaintenanceCancel
	a.publicGeoMaintenanceMu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.publicGeoMaintenanceWG.Wait()
}

func (a *App) runPublicGeoMaintenance(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	a.publicGeoConfigMu.Lock()
	defer a.publicGeoConfigMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	settings, err := a.ensurePublicGeoIPSettings(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load GeoIP settings for maintenance")
		return
	}
	changed := false
	runtimeReady := a.publicGeoIPRuntimeMatches(settings)
	if settings.Enabled != 0 && strings.TrimSpace(settings.MaxmindAccountID) != "" && strings.TrimSpace(settings.MaxmindLicenseKey) != "" &&
		(!runtimeReady || publicGeoRefreshDue(settings.LastUpdateSuccessAt, now)) {
		info, refreshErr := a.performPublicGeoIPRefresh(ctx, settings.MaxmindAccountID, settings.MaxmindLicenseKey)
		if refreshErr != nil {
			log.Warn().Err(refreshErr).Msg("Background GeoIP database refresh failed")
		} else {
			completedAt := time.Now().UTC()
			if _, err := a.DB.SetPublicGeoIpUpdateSuccess(ctx, db.SetPublicGeoIpUpdateSuccessParams{
				DatabaseType:        info.DatabaseType,
				DatabaseBuildAt:     sql.NullTime{Time: info.BuildAt, Valid: true},
				LastUpdateAttemptAt: sql.NullTime{Time: completedAt, Valid: true},
				LastUpdateSuccessAt: sql.NullTime{Time: completedAt, Valid: true},
			}); err != nil {
				log.Warn().Err(err).Msg("Failed to record background GeoIP refresh")
			} else {
				changed = true
			}
		}
	}

	sources, err := a.DB.ListPublicTrustedProxySources(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load trusted proxy sources for maintenance")
		return
	}
	for _, source := range sources {
		if source.BuiltIn == 0 || source.Enabled == 0 || !publicGeoRefreshDue(source.LastRefreshSuccessAt, now) {
			continue
		}
		if _, err := a.refreshPublicTrustedProxySource(ctx, source); err != nil {
			log.Warn().Err(err).Str("provider", source.Provider).Msg("Background trusted proxy range refresh failed")
			continue
		}
		changed = true
	}
	if changed {
		if err := a.refreshPublicProxySnapshot(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to apply refreshed GeoIP/trusted proxy configuration")
		}
	}
}

func publicGeoRefreshDue(lastSuccess sql.NullTime, now time.Time) bool {
	return !lastSuccess.Valid || lastSuccess.Time.IsZero() || now.Sub(lastSuccess.Time) >= publicGeoRefreshAge
}
