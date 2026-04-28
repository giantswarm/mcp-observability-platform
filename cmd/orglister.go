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
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// kubernetesSyncPeriod ≈ Grafana's org-sync cadence; tighter doesn't change
// observable freshness, looser risks lagging org adds during onboarding.
const kubernetesSyncPeriod = 60 * time.Second

// buildOrgCache builds the controller-runtime cache, primes the
// GrafanaOrganization informer, starts the cache, and waits for initial
// sync. Returns the org-listing port and an alive flag the readiness
// probe gates on — the flag flips false when ctrlCache.Start exits with
// a non-canceled error so /readyz returns 503 instead of serving stale
// data from the still-readable cache.
func buildOrgCache(ctx context.Context, logger *slog.Logger) (authz.OrgLister, *atomic.Bool, error) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(obsv1alpha2.AddToScheme(scheme))

	kubeCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("kube config: %w", err)
	}
	syncPeriod := kubernetesSyncPeriod
	c, err := ctrlcache.New(kubeCfg, ctrlcache.Options{
		Scheme:     scheme,
		SyncPeriod: &syncPeriod,
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
		// Start returns context.Canceled on clean shutdown; only non-cancel
		// exits indicate a crashed informer and should fail the readiness
		// probe via the alive gate. WaitForCacheSync below catches startup
		// failure separately.
		if err := c.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("controller-runtime cache stopped", "error", err)
			alive.Store(false)
		}
	}()
	if !c.WaitForCacheSync(ctx) {
		return nil, nil, fmt.Errorf("cache sync timed out")
	}
	logger.Info("GrafanaOrganization cache synced")
	return k8sOrgLister{reader: c}, &alive, nil
}

// k8sOrgLister adapts a controller-runtime cache to authz.OrgLister so the
// authz package never imports observability-operator or controller-runtime
// — this is the K8s ↔ domain translation boundary.
type k8sOrgLister struct{ reader ctrlclient.Reader }

func (k k8sOrgLister) List(ctx context.Context) ([]authz.Organization, error) {
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
		datasources := make([]grafana.Datasource, 0, len(cr.Status.DataSources))
		for _, ds := range cr.Status.DataSources {
			datasources = append(datasources, grafana.Datasource{ID: ds.ID, Name: ds.Name})
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
