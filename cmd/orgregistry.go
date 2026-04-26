package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// kubernetesSyncPeriod balances cache freshness against API-server load.
var kubernetesSyncPeriod = 60 * time.Second

// buildOrgCache builds the controller-runtime cache, primes the
// GrafanaOrganization informer, starts the cache, and waits for initial
// sync. cacheAlive flips false when ctrlCache.Start exits with a
// non-canceled error so the readiness probe can return 503.
func buildOrgCache(ctx context.Context, logger *slog.Logger) (ctrlcache.Cache, *atomic.Bool, error) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(obsv1alpha2.AddToScheme(scheme))

	kubeCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("kube config: %w", err)
	}
	c, err := ctrlcache.New(kubeCfg, ctrlcache.Options{
		Scheme:     scheme,
		SyncPeriod: &kubernetesSyncPeriod,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("controller-runtime cache: %w", err)
	}
	if _, err := c.GetInformer(ctx, &obsv1alpha2.GrafanaOrganization{}); err != nil {
		return nil, nil, fmt.Errorf("get informer: %w", err)
	}

	var alive atomic.Bool
	alive.Store(true)
	go func() {
		if err := c.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("controller-runtime cache stopped", "error", err)
			alive.Store(false)
		}
	}()
	if !c.WaitForCacheSync(ctx) {
		return nil, nil, fmt.Errorf("cache sync timed out")
	}
	logger.Info("GrafanaOrganization cache synced")
	return c, &alive, nil
}

// startOrgCacheReporter polls every 30s to keep the OrgCacheSize gauge
// accurate. The informer is event-driven internally; this loop only
// refreshes the exported metric.
func startOrgCacheReporter(ctx context.Context, c ctrlcache.Cache) {
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				var list obsv1alpha2.GrafanaOrganizationList
				if err := c.List(ctx, &list); err == nil {
					observability.OrgCacheSize.Set(float64(len(list.Items)))
				}
			}
		}
	}()
}

// listOrgCount returns the current count of GrafanaOrganization CRs.
// Health-handler adapter so the health package stays free of K8s imports.
func listOrgCount(c ctrlcache.Cache) func(context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		var list obsv1alpha2.GrafanaOrganizationList
		if err := c.List(ctx, &list); err != nil {
			return 0, err
		}
		return len(list.Items), nil
	}
}

// k8sOrgRegistry adapts a controller-runtime cache to authz.OrgRegistry.
// Lives here so the authz package never imports observability-operator
// or controller-runtime — this is the K8s ↔ domain translation boundary.
type k8sOrgRegistry struct{ reader ctrlclient.Reader }

// List implements authz.OrgRegistry.
func (k k8sOrgRegistry) List(ctx context.Context) ([]authz.Organization, error) {
	var list obsv1alpha2.GrafanaOrganizationList
	if err := k.reader.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make([]authz.Organization, len(list.Items))
	for i := range list.Items {
		cr := &list.Items[i]
		tenants := make([]authz.Tenant, 0, len(cr.Spec.Tenants))
		for _, t := range cr.Spec.Tenants {
			types := make([]authz.TenantType, 0, len(t.Types))
			for _, tt := range t.Types {
				types = append(types, authz.TenantType(tt))
			}
			tenants = append(tenants, authz.Tenant{Name: string(t.Name), Types: types})
		}
		datasources := make([]authz.Datasource, 0, len(cr.Status.DataSources))
		for _, ds := range cr.Status.DataSources {
			datasources = append(datasources, authz.Datasource{ID: ds.ID, Name: ds.Name})
		}
		out[i] = authz.Organization{
			Name:        cr.Name,
			DisplayName: cr.Spec.DisplayName,
			OrgID:       cr.Status.OrgID,
			Tenants:     tenants,
			Datasources: datasources,
		}
	}
	return out, nil
}
