package ytconfig

import (
	"encoding/json"
	"fmt"
	ytv1 "github.com/ytsaurus/yt-k8s-operator/api/v1"
	"github.com/ytsaurus/yt-k8s-operator/pkg/consts"
	"go.ytsaurus.tech/yt/go/yson"
	ptr "k8s.io/utils/pointer"
)

type GeneratorFunc func() ([]byte, error)

type Generator struct {
	ytsaurus      *ytv1.Ytsaurus
	clusterDomain string
}

func NewGenerator(ytsaurus *ytv1.Ytsaurus, clusterDomain string) *Generator {
	return &Generator{
		ytsaurus:      ytsaurus,
		clusterDomain: clusterDomain,
	}
}

func (g *Generator) getMasterAddresses() []string {
	names := make([]string, 0, g.ytsaurus.Spec.PrimaryMasters.InstanceCount)
	for _, podName := range g.GetMasterPodNames() {
		names = append(names, fmt.Sprintf("%s.%s.%s.svc.%s:%d",
			podName,
			g.GetMastersServiceName(),
			g.ytsaurus.Namespace,
			g.clusterDomain,
			consts.MasterRPCPort))
	}
	return names
}

func (g *Generator) getMasterHydraPeers() []HydraPeer {
	peers := make([]HydraPeer, 0, g.ytsaurus.Spec.PrimaryMasters.InstanceCount)
	for _, address := range g.getMasterAddresses() {
		peers = append(peers, HydraPeer{
			Address: address,
			Voting:  true,
		})
	}
	return peers
}

func (g *Generator) getDiscoveryAddresses() []string {
	names := make([]string, 0, g.ytsaurus.Spec.Discovery.InstanceCount)
	for _, podName := range g.GetDiscoveryPodNames() {
		names = append(names, fmt.Sprintf("%s.%s.%s.svc.%s:%d",
			podName,
			g.GetDiscoveryServiceName(),
			g.ytsaurus.Namespace,
			g.clusterDomain,
			consts.DiscoveryRPCPort))
	}
	return names
}

func (g *Generator) GetYQLAgentAddresses() []string {
	names := make([]string, 0, g.ytsaurus.Spec.YQLAgents.InstanceCount)
	for _, podName := range g.GetYQLAgentPodNames() {
		names = append(names, fmt.Sprintf("%s.%s.%s.svc.%s:%d",
			podName,
			g.GetYQLAgentServiceName(),
			g.ytsaurus.Namespace,
			g.clusterDomain,
			consts.YQLAgentRPCPort))
	}
	return names
}

func (g *Generator) fillDriver(c *Driver) {
	c.TimestampProviders.Addresses = g.getMasterAddresses()

	c.PrimaryMaster.Addresses = g.getMasterAddresses()
	c.PrimaryMaster.CellID = generateCellID(g.ytsaurus.Spec.PrimaryMasters.CellTag)

	c.MasterCache.EnableMasterCacheDiscover = true
	g.fillPrimaryMaster(&c.MasterCache.MasterCell)
}

func (g *Generator) fillAddressResolver(c *AddressResolver) {
	var retries = 1000

	c.EnableIPv4 = !g.ytsaurus.Spec.UseIPv6
	c.EnableIPv6 = g.ytsaurus.Spec.UseIPv6
	c.Retries = &retries
}

func (g *Generator) fillPrimaryMaster(c *MasterCell) {
	c.Addresses = g.getMasterAddresses()
	c.Peers = g.getMasterHydraPeers()
	c.CellID = generateCellID(g.ytsaurus.Spec.PrimaryMasters.CellTag)
}

func (g *Generator) fillClusterConnection(c *ClusterConnection) {
	g.fillPrimaryMaster(&c.PrimaryMaster)
	c.ClusterName = g.ytsaurus.Name
	c.DiscoveryConnection.Addresses = g.getDiscoveryAddresses()
}

func (g *Generator) fillCommonService(c *CommonServer) {
	// ToDo(psushin): enable porto resource tracker?
	g.fillAddressResolver(&c.AddressResolver)
	g.fillClusterConnection(&c.ClusterConnection)
	c.TimestampProviders.Addresses = g.getMasterAddresses()
}

func marshallYsonConfig(c interface{}) ([]byte, error) {
	result, err := yson.MarshalFormat(c, yson.FormatPretty)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func marshallJSONConfig(c interface{}) ([]byte, error) {
	result, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (g *Generator) GetClusterConnection() ([]byte, error) {
	var c ClusterConnection
	g.fillClusterConnection(&c)
	return marshallYsonConfig(c)
}

func (g *Generator) GetStrawberryControllerConfig() ([]byte, error) {
	c := getStrawberryController()
	c.LocationProxies = []string{
		g.GetHTTPProxiesAddress(consts.DefaultHTTPProxyRole),
	}
	return marshallYsonConfig(c)
}

func (g *Generator) GetChytInitClusterConfig() ([]byte, error) {
	c := getChytInitCluster()
	c.Proxy = g.GetHTTPProxiesAddress(consts.DefaultHTTPProxyRole)
	return marshallYsonConfig(c)
}

func (g *Generator) getMasterConfigImpl() (MasterServer, error) {
	c, err := getMasterServerCarcass(g.ytsaurus.Spec.PrimaryMasters)
	if err != nil {
		return MasterServer{}, err
	}
	g.fillCommonService(&c.CommonServer)
	g.fillPrimaryMaster(&c.PrimaryMaster)
	configureMasterServerCypressManager(g.ytsaurus.Spec, &c.CypressManager)
	return c, nil
}

func (g *Generator) GetMasterConfig() ([]byte, error) {
	c, err := g.getMasterConfigImpl()
	if err != nil {
		return nil, err
	}
	return marshallYsonConfig(c)
}

func (g *Generator) GetNativeClientConfig() ([]byte, error) {
	c, err := getNativeClientCarcass()
	if err != nil {
		return nil, err
	}

	g.fillDriver(&c.Driver)
	g.fillAddressResolver(&c.AddressResolver)
	c.Driver.APIVersion = 4

	return marshallYsonConfig(c)
}

func (g *Generator) getSchedulerConfigImpl() (SchedulerServer, error) {
	c, err := getSchedulerServerCarcass(*g.ytsaurus.Spec.Schedulers)
	if err != nil {
		return SchedulerServer{}, err
	}

	if g.ytsaurus.Spec.TabletNodes == nil {
		c.Scheduler.OperationsCleaner.EnableOperationArchivation = ptr.Bool(false)
	}
	g.fillCommonService(&c.CommonServer)
	return c, nil
}

func (g *Generator) GetSchedulerConfig() ([]byte, error) {
	if g.ytsaurus.Spec.Schedulers == nil {
		return []byte{}, nil
	}

	c, err := g.getSchedulerConfigImpl()
	if err != nil {
		return nil, err
	}

	return marshallYsonConfig(c)
}

func (g *Generator) getRPCProxyConfigImpl(spec ytv1.RPCProxiesSpec) (RPCProxyServer, error) {
	c, err := getRPCProxyServerCarcass(spec)
	if err != nil {
		return RPCProxyServer{}, err
	}

	g.fillCommonService(&c.CommonServer)
	return c, nil
}

func (g *Generator) GetRPCProxyConfig(spec ytv1.RPCProxiesSpec) ([]byte, error) {
	c, err := g.getRPCProxyConfigImpl(spec)
	if err != nil {
		return []byte{}, err
	}

	return marshallYsonConfig(c)
}

func (g *Generator) getTCPProxyConfigImpl(spec ytv1.TCPProxiesSpec) (TCPProxyServer, error) {
	c, err := getTCPProxyServerCarcass(spec)
	if err != nil {
		return TCPProxyServer{}, err
	}

	g.fillCommonService(&c.CommonServer)

	return c, nil
}

func (g *Generator) GetTCPProxyConfig(spec ytv1.TCPProxiesSpec) ([]byte, error) {
	if g.ytsaurus.Spec.TCPProxies == nil {
		return []byte{}, nil
	}

	c, err := g.getTCPProxyConfigImpl(spec)
	if err != nil {
		return []byte{}, err
	}

	return marshallYsonConfig(c)
}

func (g *Generator) getControllerAgentConfigImpl() (ControllerAgentServer, error) {
	c, err := getControllerAgentServerCarcass(*g.ytsaurus.Spec.ControllerAgents)
	if err != nil {
		return ControllerAgentServer{}, err
	}

	c.ControllerAgent.EnableTmpfs = g.ytsaurus.Spec.UsePorto
	c.ControllerAgent.UseColumnarStatisticsDefault = true

	g.fillCommonService(&c.CommonServer)

	return c, nil
}

func (g *Generator) GetControllerAgentConfig() ([]byte, error) {
	if g.ytsaurus.Spec.ControllerAgents == nil {
		return []byte{}, nil
	}

	c, err := g.getControllerAgentConfigImpl()
	if err != nil {
		return []byte{}, err
	}

	return marshallYsonConfig(c)
}

func (g *Generator) getDataNodeConfigImpl(spec ytv1.DataNodesSpec) (DataNodeServer, error) {
	c, err := getDataNodeServerCarcass(spec)
	if err != nil {
		return DataNodeServer{}, err
	}

	g.fillCommonService(&c.CommonServer)
	return c, nil
}

func (g *Generator) GetDataNodeConfig(spec ytv1.DataNodesSpec) ([]byte, error) {
	c, err := g.getDataNodeConfigImpl(spec)
	if err != nil {
		return []byte{}, err
	}
	return marshallYsonConfig(c)
}

func (g *Generator) getExecNodeConfigImpl(spec ytv1.ExecNodesSpec) (ExecNodeServer, error) {
	c, err := getExecNodeServerCarcass(
		spec,
		g.ytsaurus.Spec.UsePorto)
	if err != nil {
		return c, err
	}
	g.fillCommonService(&c.CommonServer)
	return c, nil
}

func (g *Generator) GetExecNodeConfig(spec ytv1.ExecNodesSpec) ([]byte, error) {
	c, err := g.getExecNodeConfigImpl(spec)
	if err != nil {
		return []byte{}, err
	}
	return marshallYsonConfig(c)
}

func (g *Generator) getTabletNodeConfigImpl(spec ytv1.TabletNodesSpec) (TabletNodeServer, error) {
	c, err := getTabletNodeServerCarcass(spec)
	if err != nil {
		return c, err
	}
	g.fillCommonService(&c.CommonServer)
	return c, nil
}

func (g *Generator) GetTabletNodeConfig(spec ytv1.TabletNodesSpec) ([]byte, error) {
	c, err := g.getTabletNodeConfigImpl(spec)
	if err != nil {
		return nil, err
	}
	return marshallYsonConfig(c)
}

func (g *Generator) getHTTPProxyConfigImpl(spec ytv1.HTTPProxiesSpec) (HTTPProxyServer, error) {
	c, err := getHTTPProxyServerCarcass(spec)
	if err != nil {
		return c, err
	}

	g.fillDriver(&c.Driver)
	g.fillCommonService(&c.CommonServer)
	return c, nil
}

func (g *Generator) GetHTTPProxyConfig(spec ytv1.HTTPProxiesSpec) ([]byte, error) {
	c, err := g.getHTTPProxyConfigImpl(spec)
	if err != nil {
		return nil, err
	}

	return marshallYsonConfig(c)
}

func (g *Generator) getQueryTrackerConfigImpl() (QueryTrackerServer, error) {
	c, err := getQueryTrackerServerCarcass(*g.ytsaurus.Spec.QueryTrackers)
	if err != nil {
		return c, err
	}
	g.fillCommonService(&c.CommonServer)

	return c, nil
}

func (g *Generator) GetQueryTrackerConfig() ([]byte, error) {
	if g.ytsaurus.Spec.QueryTrackers == nil {
		return []byte{}, nil
	}

	c, err := g.getQueryTrackerConfigImpl()
	if err != nil {
		return nil, err
	}

	return marshallYsonConfig(c)
}

func (g *Generator) getYQLAgentConfigImpl() (YQLAgentServer, error) {
	c, err := getYQLAgentServerCarcass(*g.ytsaurus.Spec.YQLAgents)
	if err != nil {
		return c, err
	}
	g.fillCommonService(&c.CommonServer)
	c.YQLAgent.AdditionalClusters = map[string]string{
		g.ytsaurus.Name: g.GetHTTPProxiesServiceAddress(consts.DefaultHTTPProxyRole),
	}
	c.YQLAgent.DefaultCluster = g.ytsaurus.Name
	return c, nil
}

func (g *Generator) GetYQLAgentConfig() ([]byte, error) {
	if g.ytsaurus.Spec.YQLAgents == nil {
		return []byte{}, nil
	}
	c, err := g.getYQLAgentConfigImpl()
	if err != nil {
		return nil, err
	}
	return marshallYsonConfig(c)
}

func (g *Generator) GetWebUIConfig() ([]byte, error) {
	if g.ytsaurus.Spec.UI == nil {
		return []byte{}, nil
	}

	c := getUIClusterCarcass()
	c.ID = g.ytsaurus.Name
	c.Name = g.ytsaurus.Name
	c.Proxy = g.GetHTTPProxiesAddress(consts.DefaultHTTPProxyRole)
	c.PrimaryMaster.CellTag = g.ytsaurus.Spec.PrimaryMasters.CellTag

	return marshallJSONConfig(WebUI{Clusters: []UICluster{c}})
}

func (g *Generator) getDiscoveryConfigImpl() (DiscoveryServer, error) {
	c, err := getDiscoveryServerCarcass(g.ytsaurus.Spec.Discovery)
	if err != nil {
		return c, err
	}

	g.fillCommonService(&c.CommonServer)
	c.DiscoveryServer.Addresses = g.getDiscoveryAddresses()
	return c, nil
}

func (g *Generator) GetDiscoveryConfig() ([]byte, error) {
	c, err := g.getDiscoveryConfigImpl()
	if err != nil {
		return nil, err
	}
	return marshallYsonConfig(c)
}
