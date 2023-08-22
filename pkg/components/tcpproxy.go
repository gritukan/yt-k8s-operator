package components

import (
	"context"
	ytv1 "github.com/ytsaurus/yt-k8s-operator/api/v1"
	"github.com/ytsaurus/yt-k8s-operator/pkg/apiproxy"
	"github.com/ytsaurus/yt-k8s-operator/pkg/consts"
	"github.com/ytsaurus/yt-k8s-operator/pkg/labeller"
	"github.com/ytsaurus/yt-k8s-operator/pkg/resources"
	"github.com/ytsaurus/yt-k8s-operator/pkg/ytconfig"
	v1 "k8s.io/api/core/v1"
)

type tcpProxy struct {
	ServerComponentBase

	master Component

	serviceType      *v1.ServiceType
	balancingService *resources.TCPService
}

func NewTCPProxy(
	cfgen *ytconfig.Generator,
	ytsaurus *apiproxy.Ytsaurus,
	masterReconciler Component,
	spec ytv1.TCPProxiesSpec) Component {
	resource := ytsaurus.GetResource()
	l := labeller.Labeller{
		ObjectMeta:     &resource.ObjectMeta,
		APIProxy:       ytsaurus.APIProxy(),
		ComponentLabel: cfgen.FormatComponentStringWithDefault(consts.YTComponentLabelTCPProxy, spec.Role),
		ComponentName:  cfgen.FormatComponentStringWithDefault("TCPProxy", spec.Role),
		MonitoringPort: consts.TCPProxyMonitoringPort,
	}

	server := NewServer(
		&l,
		ytsaurus,
		&spec.InstanceSpec,
		"/usr/bin/ytserver-tcp-proxy",
		"ytserver-tcp-proxy.yson",
		cfgen.GetTCPProxiesStatefulSetName(spec.Role),
		cfgen.GetTCPProxiesHeadlessServiceName(spec.Role),
		func() ([]byte, error) {
			return cfgen.GetTCPProxyConfig(spec)
		},
		func(data []byte) (bool, error) {
			return cfgen.NeedTCPProxyConfigReload(spec, data)
		},
	)

	var balancingService *resources.TCPService = nil
	if spec.ServiceType != nil {
		balancingService = resources.NewTCPService(
			cfgen.GetTCPProxiesServiceName(spec.Role),
			&l,
			ytsaurus.APIProxy())
	}

	return &tcpProxy{
		ServerComponentBase: ServerComponentBase{
			ComponentBase: ComponentBase{
				labeller: &l,
				ytsaurus: ytsaurus,
				cfgen:    cfgen,
			},
			server: server,
		},
		master:           masterReconciler,
		serviceType:      spec.ServiceType,
		balancingService: balancingService,
	}
}

func (r *tcpProxy) Fetch(ctx context.Context) error {
	fetchable := []resources.Fetchable{
		r.server,
	}
	if r.balancingService != nil {
		fetchable = append(fetchable, r.balancingService)
	}
	return resources.Fetch(ctx, fetchable)
}

func (r *tcpProxy) doSync(ctx context.Context, dry bool) (SyncStatus, error) {
	var err error

	if r.ytsaurus.GetClusterState() == ytv1.ClusterStateRunning && r.server.NeedUpdate() {
		return SyncStatusNeedLocalUpdate, err
	}

	if r.ytsaurus.GetClusterState() == ytv1.ClusterStateUpdating {
		if r.ytsaurus.GetUpdateState() == ytv1.UpdateStateWaitingForPodsRemoval {
			updatingComponent := r.ytsaurus.GetUpdatingComponent()
			if updatingComponent == nil || *updatingComponent == r.GetName() {
				return SyncStatusUpdating, r.removePods(ctx, dry)
			}
		}
	}

	if !(r.master.Status(ctx) == SyncStatusReady) {
		return SyncStatusBlocked, err
	}

	if r.server.NeedSync() {
		if !dry {
			err = r.server.Sync(ctx)
		}
		return SyncStatusPending, err
	}

	if r.balancingService != nil && !resources.Exists(r.balancingService) {
		if !dry {
			s := r.balancingService.Build()
			s.Spec.Type = *r.serviceType
			err = r.balancingService.Sync(ctx)
		}
		return SyncStatusPending, err
	}

	if !r.server.ArePodsReady(ctx) {
		return SyncStatusBlocked, err
	}

	return SyncStatusReady, err
}

func (r *tcpProxy) Status(ctx context.Context) SyncStatus {
	status, err := r.doSync(ctx, true)
	if err != nil {
		panic(err)
	}

	return status
}

func (r *tcpProxy) Sync(ctx context.Context) error {
	_, err := r.doSync(ctx, false)
	return err
}
