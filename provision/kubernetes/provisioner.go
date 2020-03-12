// Copyright 2016 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kubernetes

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/tsuru/config"
	"github.com/tsuru/tsuru/action"
	"github.com/tsuru/tsuru/api/shutdown"
	"github.com/tsuru/tsuru/app"
	"github.com/tsuru/tsuru/app/bind"
	tsuruErrors "github.com/tsuru/tsuru/errors"
	tsuruNet "github.com/tsuru/tsuru/net"
	"github.com/tsuru/tsuru/provision"
	"github.com/tsuru/tsuru/provision/cluster"
	tsuruv1 "github.com/tsuru/tsuru/provision/kubernetes/pkg/apis/tsuru/v1"
	"github.com/tsuru/tsuru/provision/kubernetes/provider"
	"github.com/tsuru/tsuru/provision/node"
	"github.com/tsuru/tsuru/provision/servicecommon"
	"github.com/tsuru/tsuru/servicemanager"
	"github.com/tsuru/tsuru/set"
	appTypes "github.com/tsuru/tsuru/types/app"
	provTypes "github.com/tsuru/tsuru/types/provision"
	"github.com/tsuru/tsuru/volume"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1beta1"
	v1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	v1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	provisionerName                            = "kubernetes"
	defaultKubeAPITimeout                      = time.Minute
	defaultPodReadyTimeout                     = time.Minute
	defaultPodRunningTimeout                   = 10 * time.Minute
	defaultDeploymentProgressTimeout           = 10 * time.Minute
	defaultAttachTimeoutAfterContainerFinished = time.Minute
	defaultSidecarImageName                    = "tsuru/deploy-agent:0.8.4"
)

type kubernetesProvisioner struct {
	mu                 sync.Mutex
	clusterControllers map[string]*clusterController
}

var (
	_ provision.Provisioner              = &kubernetesProvisioner{}
	_ provision.NodeProvisioner          = &kubernetesProvisioner{}
	_ provision.NodeContainerProvisioner = &kubernetesProvisioner{}
	_ provision.MessageProvisioner       = &kubernetesProvisioner{}
	_ provision.SleepableProvisioner     = &kubernetesProvisioner{}
	_ provision.VolumeProvisioner        = &kubernetesProvisioner{}
	_ provision.BuilderDeploy            = &kubernetesProvisioner{}
	_ provision.BuilderDeployKubeClient  = &kubernetesProvisioner{}
	_ provision.InitializableProvisioner = &kubernetesProvisioner{}
	_ provision.InterAppProvisioner      = &kubernetesProvisioner{}
	_ provision.HCProvisioner            = &kubernetesProvisioner{}
	_ provision.VersionsProvisioner      = &kubernetesProvisioner{}
	_ cluster.ClusteredProvisioner       = &kubernetesProvisioner{}
	_ cluster.ClusterProvider            = &kubernetesProvisioner{}
	// _ provision.OptionalLogsProvisioner  = &kubernetesProvisioner{}
	// _ provision.UnitStatusProvisioner    = &kubernetesProvisioner{}
	// _ provision.NodeRebalanceProvisioner = &kubernetesProvisioner{}
	// _ provision.AppFilterProvisioner     = &kubernetesProvisioner{}
	// _ builder.PlatformBuilder            = &kubernetesProvisioner{}
	_                         provision.UpdatableProvisioner = &kubernetesProvisioner{}
	mainKubernetesProvisioner *kubernetesProvisioner
)

func init() {
	mainKubernetesProvisioner = &kubernetesProvisioner{
		clusterControllers: map[string]*clusterController{},
	}
	provision.Register(provisionerName, func() (provision.Provisioner, error) {
		return mainKubernetesProvisioner, nil
	})
	shutdown.Register(mainKubernetesProvisioner)
}

func GetProvisioner() *kubernetesProvisioner {
	return mainKubernetesProvisioner
}

type kubernetesConfig struct {
	LogLevel           int
	DeploySidecarImage string
	DeployInspectImage string
	APITimeout         time.Duration
	// PodReadyTimeout is the timeout for a pod to become ready after already
	// running.
	PodReadyTimeout time.Duration
	// PodRunningTimeout is the timeout for a pod to become running, should
	// include time necessary to pull remote image.
	PodRunningTimeout time.Duration
	// DeploymentProgressTimeout is the timeout for a deployment to
	// successfully complete.
	DeploymentProgressTimeout time.Duration
	// AttachTimeoutAfterContainerFinished is the time tsuru will wait for an
	// attach call to finish after the attached container has finished.
	AttachTimeoutAfterContainerFinished time.Duration
	// HeadlessServicePort is the port used in headless service, by default the
	// same port number used for container is used.
	HeadlessServicePort int
	// RegisterNode if set will make tsuru add a node object to the kubernetes
	// API. Otherwise tsuru will expect the node to be already registered.
	RegisterNode bool
}

func getKubeConfig() kubernetesConfig {
	conf := kubernetesConfig{}
	conf.LogLevel, _ = config.GetInt("kubernetes:log-level")
	conf.DeploySidecarImage, _ = config.GetString("kubernetes:deploy-sidecar-image")
	if conf.DeploySidecarImage == "" {
		conf.DeploySidecarImage = defaultSidecarImageName
	}
	conf.DeployInspectImage, _ = config.GetString("kubernetes:deploy-inspect-image")
	if conf.DeployInspectImage == "" {
		conf.DeployInspectImage = defaultSidecarImageName
	}
	apiTimeout, _ := config.GetFloat("kubernetes:api-timeout")
	if apiTimeout != 0 {
		conf.APITimeout = time.Duration(apiTimeout * float64(time.Second))
	} else {
		conf.APITimeout = defaultKubeAPITimeout
	}
	podReadyTimeout, _ := config.GetFloat("kubernetes:pod-ready-timeout")
	if podReadyTimeout != 0 {
		conf.PodReadyTimeout = time.Duration(podReadyTimeout * float64(time.Second))
	} else {
		conf.PodReadyTimeout = defaultPodReadyTimeout
	}
	podRunningTimeout, _ := config.GetFloat("kubernetes:pod-running-timeout")
	if podRunningTimeout != 0 {
		conf.PodRunningTimeout = time.Duration(podRunningTimeout * float64(time.Second))
	} else {
		conf.PodRunningTimeout = defaultPodRunningTimeout
	}
	deploymentTimeout, _ := config.GetFloat("kubernetes:deployment-progress-timeout")
	if deploymentTimeout != 0 {
		conf.DeploymentProgressTimeout = time.Duration(deploymentTimeout * float64(time.Second))
	} else {
		conf.DeploymentProgressTimeout = defaultDeploymentProgressTimeout
	}
	attachTimeout, _ := config.GetFloat("kubernetes:attach-after-finish-timeout")
	if attachTimeout != 0 {
		conf.AttachTimeoutAfterContainerFinished = time.Duration(attachTimeout * float64(time.Second))
	} else {
		conf.AttachTimeoutAfterContainerFinished = defaultAttachTimeoutAfterContainerFinished
	}
	conf.HeadlessServicePort, _ = config.GetInt("kubernetes:headless-service-port")
	if conf.HeadlessServicePort == 0 {
		conf.HeadlessServicePort, _ = strconv.Atoi(provision.WebProcessDefaultPort())
	}
	conf.RegisterNode, _ = config.GetBool("kubernetes:register-node")
	return conf
}

func (p *kubernetesProvisioner) Initialize() error {
	conf := getKubeConfig()
	if conf.LogLevel > 0 {
		// These flags are used by golang/glog package which in turn is used by
		// kubernetes to control logging. Unfortunately it doesn't seem like
		// there's a better way to control glog.
		flag.CommandLine.Parse([]string{"-v", strconv.Itoa(conf.LogLevel), "-logtostderr"})
	}
	err := initAllControllers(p)
	if err == provTypes.ErrNoCluster {
		return nil
	}
	return err
}

func (p *kubernetesProvisioner) InitializeCluster(c *provTypes.Cluster) error {
	clusterClient, err := NewClusterClient(c)
	if err != nil {
		return err
	}
	stopClusterController(p, clusterClient)
	_, err = getClusterController(p, clusterClient)
	return err
}

func (p *kubernetesProvisioner) ValidateCluster(c *provTypes.Cluster) error {
	return nil
}

func (p *kubernetesProvisioner) ClusterHelp() provTypes.ClusterHelpInfo {
	createDataHelp, err := provider.FormattedCreateOptions()
	if err != nil {
		createDataHelp = map[string]string{
			"error": fmt.Sprintf("unable to get create flags: %v", err),
		}
	}
	return provTypes.ClusterHelpInfo{
		CustomDataHelp:  clusterHelp,
		CreateDataHelp:  createDataHelp,
		ProvisionerHelp: "Represents a kubernetes cluster, the address parameter must point to a valid kubernetes apiserver endpoint.",
	}
}

func (p *kubernetesProvisioner) CreateCluster(ctx context.Context, c *provTypes.Cluster) error {
	if len(c.CreateData) > 0 {
		return provider.CreateCluster(ctx, c.Name, c.CreateData, c.Writer)
	}
	return nil
}

func (p *kubernetesProvisioner) UpdateCluster(ctx context.Context, c *provTypes.Cluster) error {
	if len(c.CreateData) > 0 {
		return provider.UpdateCluster(ctx, c.Name, c.CreateData, c.Writer)
	}
	return nil
}

func (p *kubernetesProvisioner) DeleteCluster(ctx context.Context, c *provTypes.Cluster) error {
	stopClusterControllerByName(p, c.Name)
	if len(c.CreateData) > 0 {
		return provider.DeleteCluster(ctx, c.Name, c.CreateData, c.Writer)
	}
	return nil
}

func (p *kubernetesProvisioner) GetName() string {
	return provisionerName
}

func (p *kubernetesProvisioner) Provision(a provision.App) error {
	client, err := clusterForPool(a.GetPool())
	if err != nil {
		return err
	}
	return ensureAppCustomResourceSynced(client, a)
}

func (p *kubernetesProvisioner) Destroy(a provision.App) error {
	client, err := clusterForPool(a.GetPool())
	if err != nil {
		return err
	}
	tclient, err := TsuruClientForConfig(client.restConfig)
	if err != nil {
		return err
	}
	app, err := tclient.TsuruV1().Apps(client.Namespace()).Get(a.GetName(), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := p.removeResources(client, app, a); err != nil {
		return err
	}
	return tclient.TsuruV1().Apps(client.Namespace()).Delete(a.GetName(), &metav1.DeleteOptions{})
}

func (p *kubernetesProvisioner) removeResources(client *ClusterClient, tsuruApp *tsuruv1.App, app provision.App) error {
	deps, err := allDeploymentsForAppNS(client, tsuruApp.Spec.NamespaceName, app)
	if err != nil {
		return err
	}
	svcs, err := allServicesForAppNS(client, tsuruApp.Spec.NamespaceName, app)
	if err != nil {
		return err
	}
	multiErrors := tsuruErrors.NewMultiError()
	for _, dd := range deps {
		err = cleanupSingleDeployment(client, &dd)
		if err != nil {
			multiErrors.Add(err)
		}
	}
	for _, ss := range svcs {
		err = client.CoreV1().Services(tsuruApp.Spec.NamespaceName).Delete(ss.Name, &metav1.DeleteOptions{
			PropagationPolicy: propagationPtr(metav1.DeletePropagationForeground),
		})
		if err != nil && !k8sErrors.IsNotFound(err) {
			multiErrors.Add(errors.WithStack(err))
		}
	}
	vols, err := volume.ListByApp(app.GetName())
	if err != nil {
		multiErrors.Add(errors.WithStack(err))
	} else {
		for _, vol := range vols {
			_, err = vol.LoadBinds()
			if err != nil {
				continue
			}

			bindedToOtherApps := false
			for _, b := range vol.Binds {
				if b.ID.App != app.GetName() {
					bindedToOtherApps = true
					break
				}
			}
			if !bindedToOtherApps {
				err = deleteVolume(client, vol.Name)
				if err != nil {
					multiErrors.Add(errors.WithStack(err))
				}
			}
		}
	}
	err = client.CoreV1().ServiceAccounts(tsuruApp.Spec.NamespaceName).Delete(tsuruApp.Spec.ServiceAccountName, &metav1.DeleteOptions{})
	if err != nil && !k8sErrors.IsNotFound(err) {
		multiErrors.Add(errors.WithStack(err))
	}
	return multiErrors.ToError()
}

func versionsForAppProcess(client *ClusterClient, a provision.App, process string) ([]appTypes.AppVersion, error) {
	deps, err := allDeploymentsForApp(client, a)
	if err != nil {
		return nil, err
	}

	versionSet := map[int]struct{}{}
	for _, dep := range deps {
		ls := labelSetFromMeta(&dep.ObjectMeta)
		if process == "" || process == ls.AppProcess() {
			versionSet[ls.Version()] = struct{}{}
		}
	}

	var versions []appTypes.AppVersion
	for v := range versionSet {
		version, err := servicemanager.AppVersion.VersionByImageOrVersion(a, strconv.Itoa(v))
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, nil
}

func changeState(a provision.App, process string, version appTypes.AppVersion, state servicecommon.ProcessState, w io.Writer) error {
	client, err := clusterForPool(a.GetPool())
	if err != nil {
		return err
	}
	err = ensureAppCustomResourceSynced(client, a)
	if err != nil {
		return err
	}

	var versions []appTypes.AppVersion
	if version == nil {
		versions, err = versionsForAppProcess(client, a, process)
		if err != nil {
			return err
		}
	} else {
		versions = append(versions, version)
	}

	var multiErr tsuruErrors.MultiError
	for _, v := range versions {
		err = servicecommon.ChangeAppState(&serviceManager{
			client: client,
			writer: w,
		}, a, process, state, v)
		if err != nil {
			multiErr.Add(errors.Wrapf(err, "unable to update version v%d", v.Version()))
		}
	}
	return multiErr.ToError()
}

func changeUnits(a provision.App, units int, processName string, version appTypes.AppVersion, w io.Writer) error {
	client, err := clusterForPool(a.GetPool())
	if err != nil {
		return err
	}
	err = ensureAppCustomResourceSynced(client, a)
	if err != nil {
		return err
	}
	return servicecommon.ChangeUnits(&serviceManager{
		client: client,
		writer: w,
	}, a, units, processName, version)
}

func (p *kubernetesProvisioner) AddUnits(a provision.App, units uint, processName string, version appTypes.AppVersion, w io.Writer) error {
	return changeUnits(a, int(units), processName, version, w)
}

func (p *kubernetesProvisioner) RemoveUnits(a provision.App, units uint, processName string, version appTypes.AppVersion, w io.Writer) error {
	return changeUnits(a, -int(units), processName, version, w)
}

func (p *kubernetesProvisioner) Restart(a provision.App, process string, version appTypes.AppVersion, w io.Writer) error {
	return changeState(a, process, version, servicecommon.ProcessState{Start: true, Restart: true}, w)
}

func (p *kubernetesProvisioner) Start(a provision.App, process string, version appTypes.AppVersion) error {
	return changeState(a, process, version, servicecommon.ProcessState{Start: true}, nil)
}

func (p *kubernetesProvisioner) Stop(a provision.App, process string, version appTypes.AppVersion) error {
	return changeState(a, process, version, servicecommon.ProcessState{Stop: true}, nil)
}

func (p *kubernetesProvisioner) Sleep(a provision.App, process string, version appTypes.AppVersion) error {
	return changeState(a, process, version, servicecommon.ProcessState{Stop: true, Sleep: true}, nil)
}

var stateMap = map[apiv1.PodPhase]provision.Status{
	apiv1.PodPending:   provision.StatusCreated,
	apiv1.PodRunning:   provision.StatusStarted,
	apiv1.PodSucceeded: provision.StatusStopped,
	apiv1.PodFailed:    provision.StatusError,
	apiv1.PodUnknown:   provision.StatusError,
}

func (p *kubernetesProvisioner) podsToUnits(client *ClusterClient, pods []apiv1.Pod, baseApp provision.App, baseNode *apiv1.Node) ([]provision.Unit, error) {
	var apps []provision.App
	if baseApp != nil {
		apps = append(apps, baseApp)
	}
	var nodes []apiv1.Node
	if baseNode != nil {
		nodes = append(nodes, *baseNode)
	}
	return p.podsToUnitsMultiple(client, pods, apps, nodes)
}

func (p *kubernetesProvisioner) podsToUnitsMultiple(client *ClusterClient, pods []apiv1.Pod, baseApps []provision.App, baseNodes []apiv1.Node) ([]provision.Unit, error) {
	var err error
	if len(pods) == 0 {
		return nil, nil
	}
	nodeMap := map[string]*apiv1.Node{}
	appMap := map[string]provision.App{}
	portsMap := map[string][]int32{}
	for _, baseApp := range baseApps {
		appMap[baseApp.GetName()] = baseApp
	}
	controller, err := getClusterController(p, client)
	if err != nil {
		return nil, err
	}
	nodeInformer, err := controller.getNodeInformer()
	if err != nil {
		return nil, err
	}
	if len(baseNodes) == 0 {
		baseNodes, err = nodesForPods(nodeInformer, pods)
		if err != nil {
			return nil, err
		}
	}
	for i, baseNode := range baseNodes {
		nodeMap[baseNode.Name] = &baseNodes[i]
	}
	svcInformer, err := controller.getServiceInformer()
	if err != nil {
		return nil, err
	}
	var units []provision.Unit
	for _, pod := range pods {
		if isTerminating(pod) || isEvicted(pod) {
			continue
		}
		l := labelSetFromMeta(&pod.ObjectMeta)
		node, ok := nodeMap[pod.Spec.NodeName]
		if !ok && pod.Spec.NodeName != "" {
			node, err = nodeInformer.Lister().Get(pod.Spec.NodeName)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			nodeMap[pod.Spec.NodeName] = node.DeepCopy()
		}
		podApp, ok := appMap[l.AppName()]
		if !ok {
			podApp, err = app.GetByName(l.AppName())
			if err != nil {
				return nil, errors.WithStack(err)
			}
			appMap[podApp.GetName()] = podApp
		}
		wrapper := kubernetesNodeWrapper{node: node, prov: p}
		u := &url.URL{
			Scheme: "http",
			Host:   wrapper.Address(),
		}
		urls := []url.URL{}
		appProcess := l.AppProcess()
		if appProcess != "" {
			srvName := serviceNameForApp(podApp, appProcess, l.Version())
			ports, ok := portsMap[srvName]
			if !ok {
				ports, err = getServicePorts(svcInformer, srvName, pod.ObjectMeta.Namespace)
				if err != nil {
					return nil, err
				}
				portsMap[srvName] = ports
			}
			if len(ports) > 0 {
				u.Host = fmt.Sprintf("%s:%d", u.Host, ports[0])
				for _, p := range ports {
					urls = append(urls, url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", wrapper.Address(), p)})
				}
			}
		}
		units = append(units, provision.Unit{
			ID:          pod.Name,
			Name:        pod.Name,
			AppName:     l.AppName(),
			ProcessName: appProcess,
			Type:        l.AppPlatform(),
			IP:          wrapper.ip(),
			Status:      stateMap[pod.Status.Phase],
			Address:     u,
			Addresses:   urls,
			Version:     l.Version(),
			Routable:    l.IsRoutable(),
		})
	}
	return units, nil
}

// merged from https://github.com/kubernetes/kubernetes/blob/1f69c34478800e150acd022f6313a15e1cb7a97c/pkg/quota/evaluator/core/pods.go#L333
// and https://github.com/kubernetes/kubernetes/blob/560e15fb9acee4b8391afbc21fc3aea7b771e2c4/pkg/printers/internalversion/printers.go#L606
func isTerminating(pod apiv1.Pod) bool {
	return pod.Spec.ActiveDeadlineSeconds != nil && *pod.Spec.ActiveDeadlineSeconds >= int64(0) || pod.DeletionTimestamp != nil
}

func isEvicted(pod apiv1.Pod) bool {
	return pod.Status.Phase == apiv1.PodFailed && strings.ToLower(pod.Status.Reason) == "evicted"
}

func nodesForPods(informer v1informers.NodeInformer, pods []apiv1.Pod) ([]apiv1.Node, error) {
	nodeSet := map[string]struct{}{}
	for _, p := range pods {
		nodeSet[p.Spec.NodeName] = struct{}{}
	}
	nodes, err := informer.Lister().List(labels.Everything())
	if err != nil {
		return nil, errors.WithStack(err)
	}
	nodesRet := make([]apiv1.Node, 0, len(nodes))
	for _, node := range nodes {
		_, inSet := nodeSet[node.Name]
		if inSet {
			copy := node.DeepCopy()
			nodesRet = append(nodesRet, *copy)
		}
	}
	return nodesRet, nil
}

func (p *kubernetesProvisioner) Units(apps ...provision.App) ([]provision.Unit, error) {
	cApps, err := clustersForApps(apps)
	if err != nil {
		return nil, err
	}
	var units []provision.Unit
	for _, cApp := range cApps {
		pods, err := p.podsForApps(cApp.client, cApp.apps)
		if err != nil {
			return nil, err
		}
		clusterUnits, err := p.podsToUnitsMultiple(cApp.client, pods, cApp.apps, nil)
		if err != nil {
			return nil, err
		}
		units = append(units, clusterUnits...)
	}
	return units, nil
}

func (p *kubernetesProvisioner) podsForApps(client *ClusterClient, apps []provision.App) ([]apiv1.Pod, error) {
	inSelectorMap := map[string][]string{}
	for _, a := range apps {
		l, err := provision.ServiceLabels(provision.ServiceLabelsOpts{
			App: a,
			ServiceLabelExtendedOpts: provision.ServiceLabelExtendedOpts{
				Prefix:      tsuruLabelPrefix,
				Provisioner: provisionerName,
			},
		})
		if err != nil {
			return nil, err
		}
		appSel := l.ToAppSelector()
		for k, v := range appSel {
			inSelectorMap[k] = append(inSelectorMap[k], v)
		}
	}
	sel := labels.NewSelector()
	for k, v := range inSelectorMap {
		if len(v) == 0 {
			continue
		}
		req, err := labels.NewRequirement(k, selection.In, v)
		if err != nil {
			return nil, err
		}
		sel = sel.Add(*req)
	}
	controller, err := getClusterController(p, client)
	if err != nil {
		return nil, err
	}
	informer, err := controller.getPodInformer()
	if err != nil {
		return nil, err
	}
	pods, err := informer.Lister().List(sel)
	if err != nil {
		return nil, err
	}
	podCopies := make([]apiv1.Pod, len(pods))
	for i, p := range pods {
		podCopies[i] = *p.DeepCopy()
	}
	return podCopies, nil
}

func (p *kubernetesProvisioner) RoutableAddresses(a provision.App) ([]appTypes.RoutableAddresses, error) {
	client, err := clusterForPool(a.GetPool())
	if err != nil {
		return nil, err
	}
	version, err := servicemanager.AppVersion.LatestSuccessfulVersion(a)
	if err != nil {
		if err != appTypes.ErrNoVersionsAvailable {
			return nil, err
		}
		return nil, nil
	}
	processes, err := version.Processes()
	if err != nil {
		return nil, err
	}
	webProcessName, err := version.WebProcess()
	if err != nil {
		return nil, err
	}
	controller, err := getClusterController(p, client)
	if err != nil {
		return nil, err
	}
	svcInformer, err := controller.getServiceInformer()
	if err != nil {
		return nil, err
	}
	ns, err := client.AppNamespace(a)
	if err != nil {
		return nil, err
	}
	var allAddrs []appTypes.RoutableAddresses
	for processName := range processes {
		var deployments []appsv1.Deployment
		deployments, err = allDeploymentsForAppProcess(client, a, processName)
		if err != nil {
			return nil, err
		}
		var rAddr appTypes.RoutableAddresses

		if processName == webProcessName {
			rAddr, err = p.routableAddrForProcess(client, a, processName, ns, "", 0, svcInformer)
			if err != nil {
				return nil, err
			}
			allAddrs = append(allAddrs, rAddr)
		}

		prefix := fmt.Sprintf("%s.process", processName)
		rAddr, err = p.routableAddrForProcess(client, a, processName, ns, prefix, 0, svcInformer)
		if err != nil {
			return nil, err
		}
		allAddrs = append(allAddrs, rAddr)

		for _, dep := range deployments {
			labels := labelSetFromMeta(&dep.ObjectMeta)
			activeVersion := labels.Version()
			if activeVersion == 0 {
				continue
			}
			prefix = fmt.Sprintf("v%d.version.%s.process", activeVersion, processName)
			rAddr, err = p.routableAddrForProcess(client, a, processName, ns, prefix, activeVersion, svcInformer)
			if err != nil {
				return nil, err
			}
			allAddrs = append(allAddrs, rAddr)

			if processName == webProcessName {
				prefix = fmt.Sprintf("v%d.version", activeVersion)
				rAddr, err = p.routableAddrForProcess(client, a, processName, ns, prefix, activeVersion, svcInformer)
				if err != nil {
					return nil, err
				}
				allAddrs = append(allAddrs, rAddr)
			}
		}
	}
	return allAddrs, nil
}

func (p *kubernetesProvisioner) routableAddrForProcess(client *ClusterClient, a provision.App, processName, ns, prefix string, version int, svcInformer v1informers.ServiceInformer) (appTypes.RoutableAddresses, error) {
	var routableAddrs appTypes.RoutableAddresses
	srvName := serviceNameForApp(a, processName, version)
	pubPort, err := getServicePort(svcInformer, srvName, ns)
	if err != nil {
		return routableAddrs, err
	}
	if pubPort == 0 {
		return routableAddrs, nil
	}
	routerLocal, err := client.RouterAddressLocal(a.GetPool())
	if err != nil {
		return routableAddrs, err
	}
	var addrs []*url.URL
	if routerLocal {
		addrs, err = p.addressesForApp(client, a, processName, pubPort, version)
	} else {
		addrs, err = p.addressesForPool(client, a.GetPool(), pubPort)
	}
	if err != nil || addrs == nil {
		return routableAddrs, err
	}
	return appTypes.RoutableAddresses{
		Prefix:    prefix,
		Addresses: addrs,
		ExtraData: map[string]string{
			"service":   srvName,
			"namespace": ns,
		},
	}, nil
}

func (p *kubernetesProvisioner) addressesForApp(client *ClusterClient, a provision.App, processName string, pubPort int32, version int) ([]*url.URL, error) {
	pods, err := p.podsForApps(client, []provision.App{a})
	if err != nil {
		return nil, err
	}
	controller, err := getClusterController(p, client)
	if err != nil {
		return nil, err
	}
	nodeInformer, err := controller.getNodeInformer()
	if err != nil {
		return nil, err
	}
	addrs := make([]*url.URL, 0)
	for _, pod := range pods {
		labelSet := labelSetFromMeta(&pod.ObjectMeta)
		if labelSet.IsIsolatedRun() {
			continue
		}
		if labelSet.AppProcess() != processName {
			continue
		}
		if version != 0 && labelSet.Version() != version {
			continue
		}
		if isPodReady(&pod) {
			node, err := nodeInformer.Lister().Get(pod.Spec.NodeName)
			if err != nil {
				return nil, err
			}
			wrapper := kubernetesNodeWrapper{node: node, prov: p}
			addrs = append(addrs, &url.URL{
				Scheme: "http",
				Host:   fmt.Sprintf("%s:%d", wrapper.Address(), pubPort),
			})
		}
	}
	return addrs, nil
}

func (p *kubernetesProvisioner) addressesForPool(client *ClusterClient, poolName string, pubPort int32) ([]*url.URL, error) {
	nodeSelector := provision.NodeLabels(provision.NodeLabelsOpts{
		Pool:   poolName,
		Prefix: tsuruLabelPrefix,
	}).ToNodeByPoolSelector()
	controller, err := getClusterController(p, client)
	if err != nil {
		return nil, err
	}
	nodeInformer, err := controller.getNodeInformer()
	if err != nil {
		return nil, err
	}
	nodes, err := nodeInformer.Lister().List(labels.SelectorFromSet(nodeSelector))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	addrs := make([]*url.URL, len(nodes))
	for i, n := range nodes {
		wrapper := kubernetesNodeWrapper{node: n, prov: p}
		addrs[i] = &url.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("%s:%d", wrapper.Address(), pubPort),
		}
	}
	return addrs, nil
}

func (p *kubernetesProvisioner) RegisterUnit(a provision.App, unitID string, customData map[string]interface{}) error {
	client, err := clusterForPool(a.GetPool())
	if err != nil {
		return err
	}
	ns, err := client.AppNamespace(a)
	if err != nil {
		return err
	}
	pod, err := client.CoreV1().Pods(ns).Get(unitID, metav1.GetOptions{})
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			return &provision.UnitNotFoundError{ID: unitID}
		}
		return errors.WithStack(err)
	}
	units, err := p.podsToUnits(client, []apiv1.Pod{*pod}, a, nil)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		return errors.Errorf("unable to convert pod to unit: %#v", pod)
	}
	if customData == nil {
		return nil
	}
	l := labelSetFromMeta(&pod.ObjectMeta)
	buildingImage := l.BuildImage()
	if buildingImage == "" {
		return nil
	}
	version, err := servicemanager.AppVersion.VersionByPendingImage(a, buildingImage)
	if err != nil {
		return errors.WithStack(err)
	}
	err = version.AddData(appTypes.AddVersionDataArgs{
		CustomData: customData,
	})
	return errors.WithStack(err)
}

func (p *kubernetesProvisioner) ListNodes(addressFilter []string) ([]provision.Node, error) {
	var nodes []provision.Node
	err := forEachCluster(func(c *ClusterClient) error {
		clusterNodes, err := p.listNodesForCluster(c, nodeFilter{addresses: addressFilter})
		if err != nil {
			return err
		}
		nodes = append(nodes, clusterNodes...)
		return nil
	})
	if err == provTypes.ErrNoCluster {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

func (p *kubernetesProvisioner) InternalAddresses(ctx context.Context, a provision.App) ([]provision.AppInternalAddress, error) {
	client, err := clusterForPool(a.GetPool())
	if err != nil {
		return nil, err
	}
	ns, err := client.AppNamespace(a)
	if err != nil {
		return nil, err
	}

	controller, err := getClusterController(p, client)
	if err != nil {
		return nil, err
	}
	svcInformer, err := controller.getServiceInformer()
	if err != nil {
		return nil, err
	}

	svcs, err := allServicesForAppInformer(svcInformer, ns, a)
	if err != nil {
		return nil, err
	}

	addresses := []provision.AppInternalAddress{}
	for _, service := range svcs {
		for _, port := range service.Spec.Ports {
			addresses = append(addresses, provision.AppInternalAddress{
				Domain:   fmt.Sprintf("%s.%s.svc.cluster.local", service.Name, ns),
				Protocol: string(port.Protocol),
				Port:     port.Port,
			})
		}
	}
	return addresses, nil
}

type nodeFilter struct {
	addresses []string
	metadata  map[string]string
}

func (p *kubernetesProvisioner) listNodesForCluster(cluster *ClusterClient, filter nodeFilter) ([]provision.Node, error) {
	var addressSet set.Set
	if len(filter.addresses) > 0 {
		addressSet = set.FromSlice(filter.addresses)
	}
	controller, err := getClusterController(p, cluster)
	if err != nil {
		return nil, err
	}
	nodeInformer, err := controller.getNodeInformer()
	if err != nil {
		return nil, err
	}
	nodeList, err := nodeInformer.Lister().List(labels.Everything())
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var nodes []provision.Node
	for i := range nodeList {
		n := &kubernetesNodeWrapper{
			node:    nodeList[i].DeepCopy(),
			prov:    p,
			cluster: cluster,
		}
		matchesAddresses := len(addressSet) == 0 || addressSet.Includes(n.Address())
		matchesMetadata := len(filter.metadata) == 0 || node.HasAllMetadata(n.MetadataNoPrefix(), filter.metadata)
		if matchesAddresses && matchesMetadata {
			nodes = append(nodes, n)
		}
	}
	return nodes, nil
}

func (p *kubernetesProvisioner) ListNodesByFilter(filter *provTypes.NodeFilter) ([]provision.Node, error) {
	var nodes []provision.Node
	err := forEachCluster(func(c *ClusterClient) error {
		clusterNodes, err := p.listNodesForCluster(c, nodeFilter{metadata: filter.Metadata})
		if err != nil {
			return err
		}
		nodes = append(nodes, clusterNodes...)
		return nil
	})
	if err == provTypes.ErrNoCluster {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

func (p *kubernetesProvisioner) GetNode(address string) (provision.Node, error) {
	_, node, err := p.findNodeByAddress(address)
	if err != nil {
		return nil, err
	}
	return node, nil
}

func setNodeMetadata(node *apiv1.Node, pool, iaasID string, meta map[string]string) {
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	for k, v := range meta {
		k = tsuruLabelPrefix + strings.TrimPrefix(k, tsuruLabelPrefix)
		switch k {
		case tsuruExtraAnnotationsMeta:
			appendKV(v, ",", "=", node.Annotations)
		case tsuruExtraLabelsMeta:
			appendKV(v, ",", "=", node.Labels)
		}
		if v == "" {
			delete(node.Annotations, k)
			continue
		}
		node.Annotations[k] = v
	}
	baseNodeLabels := provision.NodeLabels(provision.NodeLabelsOpts{
		IaaSID: iaasID,
		Pool:   pool,
		Prefix: tsuruLabelPrefix,
	})
	for k, v := range baseNodeLabels.ToLabels() {
		if v == "" {
			continue
		}
		delete(node.Annotations, k)
		node.Labels[k] = v
	}
}

func appendKV(s, outSep, innSep string, m map[string]string) {
	kvs := strings.Split(s, outSep)
	for _, kv := range kvs {
		parts := strings.SplitN(kv, innSep, 2)
		if len(parts) != 2 {
			continue
		}
		if parts[1] == "" {
			delete(m, parts[1])
			continue
		}
		m[parts[0]] = parts[1]
	}
}

func (p *kubernetesProvisioner) AddNode(opts provision.AddNodeOptions) (err error) {
	client, err := clusterForPool(opts.Pool)
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			servicecommon.RebuildRoutesPoolApps(opts.Pool)
		}
	}()
	hostAddr := tsuruNet.URLToHost(opts.Address)
	conf := getKubeConfig()
	if conf.RegisterNode {
		node := &apiv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: hostAddr,
			},
		}
		setNodeMetadata(node, opts.Pool, opts.IaaSID, opts.Metadata)
		_, err = client.CoreV1().Nodes().Create(node)
		if err == nil {
			return nil
		}
		if !k8sErrors.IsAlreadyExists(err) {
			return errors.WithStack(err)
		}
	}
	return p.internalNodeUpdate(provision.UpdateNodeOptions{
		Address:  hostAddr,
		Metadata: opts.Metadata,
		Pool:     opts.Pool,
	}, opts.IaaSID)
}

func (p *kubernetesProvisioner) RemoveNode(opts provision.RemoveNodeOptions) error {
	client, nodeWrapper, err := p.findNodeByAddress(opts.Address)
	if err != nil {
		return err
	}
	node := nodeWrapper.node
	if opts.Rebalance {
		node.Spec.Unschedulable = true
		_, err = client.CoreV1().Nodes().Update(node)
		if err != nil {
			return errors.WithStack(err)
		}
		var pods []apiv1.Pod
		pods, err = podsFromNode(client, node.Name, tsuruLabelPrefix+provision.LabelAppPool)
		if err != nil {
			return err
		}
		for _, pod := range pods {
			err = client.CoreV1().Pods(pod.Namespace).Evict(&policy.Eviction{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pod.Name,
					Namespace: pod.Namespace,
				},
			})
			if err != nil {
				return errors.WithStack(err)
			}
		}
	}
	err = client.CoreV1().Nodes().Delete(node.Name, &metav1.DeleteOptions{})
	if err != nil {
		return errors.WithStack(err)
	}
	servicecommon.RebuildRoutesPoolApps(nodeWrapper.Pool())
	return nil
}

func (p *kubernetesProvisioner) NodeForNodeData(nodeData provision.NodeStatusData) (provision.Node, error) {
	return node.FindNodeByAddrs(p, nodeData.Addrs)
}

func (p *kubernetesProvisioner) findNodeByAddress(address string) (*ClusterClient, *kubernetesNodeWrapper, error) {
	var (
		foundNode    *kubernetesNodeWrapper
		foundCluster *ClusterClient
	)
	err := forEachCluster(func(c *ClusterClient) error {
		if foundNode != nil {
			return nil
		}
		node, err := p.getNodeByAddr(c, address)
		if err == nil {
			foundNode = &kubernetesNodeWrapper{
				node:    node,
				prov:    p,
				cluster: c,
			}
			foundCluster = c
			return nil
		}
		if err != provision.ErrNodeNotFound {
			return err
		}
		return nil
	})
	if err != nil {
		if err == provTypes.ErrNoCluster {
			return nil, nil, provision.ErrNodeNotFound
		}
		return nil, nil, err
	}
	if foundNode == nil {
		return nil, nil, provision.ErrNodeNotFound
	}
	return foundCluster, foundNode, nil
}

func (p *kubernetesProvisioner) UpdateNode(opts provision.UpdateNodeOptions) error {
	return p.internalNodeUpdate(opts, "")
}

func (p *kubernetesProvisioner) internalNodeUpdate(opts provision.UpdateNodeOptions, iaasID string) error {
	client, nodeWrapper, err := p.findNodeByAddress(opts.Address)
	if err != nil {
		return err
	}
	if nodeWrapper.IaaSID() != "" {
		iaasID = ""
	}
	node := nodeWrapper.node
	shouldRemove := map[string]bool{
		tsuruInProgressTaint:   true,
		tsuruNodeDisabledTaint: opts.Enable,
	}
	taints := node.Spec.Taints
	var isDisabled bool
	for i := 0; i < len(taints); i++ {
		if taints[i].Key == tsuruNodeDisabledTaint {
			isDisabled = true
		}
		if remove := shouldRemove[taints[i].Key]; remove {
			taints[i] = taints[len(taints)-1]
			taints = taints[:len(taints)-1]
			i--
		}
	}
	if !isDisabled && opts.Disable {
		taints = append(taints, apiv1.Taint{
			Key:    tsuruNodeDisabledTaint,
			Effect: apiv1.TaintEffectNoSchedule,
		})
	}
	node.Spec.Taints = taints
	setNodeMetadata(node, opts.Pool, iaasID, opts.Metadata)
	_, err = client.CoreV1().Nodes().Update(node)
	return errors.WithStack(err)
}

func (p *kubernetesProvisioner) Deploy(args provision.DeployArgs) (string, error) {
	client, err := clusterForPool(args.App.GetPool())
	if err != nil {
		return "", err
	}

	if args.PreserveVersions {
		var hasLegacy bool
		hasLegacy, err = hasLegacyVersionForApp(client, args.App)
		if err != nil {
			return "", err
		}
		if hasLegacy {
			return "", errors.Errorf("unable to deploy multiple versions of app, please make a regular deploy first to enable multiple versions")
		}
	}

	if err = ensureAppCustomResourceSynced(client, args.App); err != nil {
		return "", err
	}
	if args.Version.VersionInfo().DeployImage == "" {
		deployPodName := deployPodNameForApp(args.App, args.Version)
		ns, nsErr := client.AppNamespace(args.App)
		if nsErr != nil {
			return "", nsErr
		}
		defer cleanupPod(client, deployPodName, ns)
		params := createPodParams{
			app:               args.App,
			client:            client,
			podName:           deployPodName,
			sourceImage:       args.Version.VersionInfo().BuildImage,
			destinationImages: []string{args.Version.BaseImageName()},
			attachOutput:      args.Event,
			attachInput:       strings.NewReader("."),
			inputFile:         "/dev/null",
		}
		ctx, cancel := args.Event.CancelableContext(context.Background())
		err = createDeployPod(ctx, params)
		cancel()
		if err != nil {
			return "", err
		}
		err = args.Version.CommitBaseImage()
		if err != nil {
			return "", err
		}
	}
	manager := &serviceManager{
		client: client,
		writer: args.Event,
	}
	var oldVersion appTypes.AppVersion
	if !args.PreserveVersions {
		oldVersion, err = baseVersionForApp(client, args.App)
		if err != nil {
			return "", err
		}
	}
	err = servicecommon.RunServicePipeline(manager, oldVersion, args, nil)
	if err != nil {
		return "", errors.WithStack(err)
	}
	err = ensureAppCustomResourceSynced(client, args.App)
	if err != nil {
		return "", err
	}
	return args.Version.VersionInfo().DeployImage, nil
}

func (p *kubernetesProvisioner) UpgradeNodeContainer(name string, pool string, writer io.Writer) error {
	m := nodeContainerManager{}
	return servicecommon.UpgradeNodeContainer(&m, name, pool, writer)
}

func (p *kubernetesProvisioner) RemoveNodeContainer(name string, pool string, writer io.Writer) error {
	err := forEachCluster(func(cluster *ClusterClient) error {
		return cleanupDaemonSet(cluster, name, pool)
	})
	if err == provTypes.ErrNoCluster {
		return nil
	}
	return err
}

func (p *kubernetesProvisioner) ExecuteCommand(opts provision.ExecOptions) error {
	client, err := clusterForPool(opts.App.GetPool())
	if err != nil {
		return err
	}
	var size *remotecommand.TerminalSize
	if opts.Width != 0 && opts.Height != 0 {
		size = &remotecommand.TerminalSize{
			Width:  uint16(opts.Width),
			Height: uint16(opts.Height),
		}
	}
	if opts.Term != "" {
		opts.Cmds = append([]string{"/usr/bin/env", "TERM=" + opts.Term}, opts.Cmds...)
	}
	eOpts := execOpts{
		client:   client,
		app:      opts.App,
		cmds:     opts.Cmds,
		stdout:   opts.Stdout,
		stderr:   opts.Stderr,
		stdin:    opts.Stdin,
		termSize: size,
		tty:      opts.Stdin != nil,
	}
	if len(opts.Units) == 0 {
		return runIsolatedCmdPod(context.TODO(), client, eOpts)
	}
	for _, u := range opts.Units {
		eOpts.unit = u
		err := execCommand(eOpts)
		if err != nil {
			return err
		}
	}
	return nil
}

func runIsolatedCmdPod(ctx context.Context, client *ClusterClient, opts execOpts) error {
	baseName := execCommandPodNameForApp(opts.app)
	labels, err := provision.ServiceLabels(provision.ServiceLabelsOpts{
		App: opts.app,
		ServiceLabelExtendedOpts: provision.ServiceLabelExtendedOpts{
			Prefix:        tsuruLabelPrefix,
			Provisioner:   provisionerName,
			IsIsolatedRun: true,
		},
	})
	if err != nil {
		return errors.WithStack(err)
	}
	var version appTypes.AppVersion
	if opts.image == "" {
		version, err = servicemanager.AppVersion.LatestSuccessfulVersion(opts.app)
		if err != nil {
			return errors.WithStack(err)
		}
		opts.image = version.VersionInfo().DeployImage
	}
	appEnvs := provision.EnvsForApp(opts.app, "", false, version)
	var envs []apiv1.EnvVar
	for _, envData := range appEnvs {
		envs = append(envs, apiv1.EnvVar{Name: envData.Name, Value: envData.Value})
	}
	return runPod(ctx, runSinglePodArgs{
		client:       client,
		eventsOutput: opts.eventsOutput,
		stdout:       opts.stdout,
		stderr:       opts.stderr,
		stdin:        opts.stdin,
		termSize:     opts.termSize,
		image:        opts.image,
		labels:       labels,
		cmds:         opts.cmds,
		envs:         envs,
		name:         baseName,
		app:          opts.app,
	})
}

func (p *kubernetesProvisioner) StartupMessage() (string, error) {
	clusters, err := allClusters()
	if err != nil {
		if err == provTypes.ErrNoCluster {
			return "", nil
		}
		return "", err
	}
	var out string
	for _, c := range clusters {
		nodeList, err := p.listNodesForCluster(c, nodeFilter{})
		if err != nil {
			return "", err
		}
		out += fmt.Sprintf("Kubernetes provisioner on cluster %q - %s:\n", c.Name, c.restConfig.Host)
		if len(nodeList) == 0 {
			out += "    No Kubernetes nodes available\n"
		}
		sort.Slice(nodeList, func(i, j int) bool {
			return nodeList[i].Address() < nodeList[j].Address()
		})
		for _, node := range nodeList {
			out += fmt.Sprintf("    Kubernetes node: %s\n", node.Address())
		}
	}
	return out, nil
}

func (p *kubernetesProvisioner) DeleteVolume(volumeName, pool string) error {
	client, err := clusterForPool(pool)
	if err != nil {
		return err
	}
	return deleteVolume(client, volumeName)
}

func (p *kubernetesProvisioner) IsVolumeProvisioned(volumeName, pool string) (bool, error) {
	client, err := clusterForPool(pool)
	if err != nil {
		return false, err
	}
	return volumeExists(client, volumeName)
}

func (p *kubernetesProvisioner) UpdateApp(old, new provision.App, w io.Writer) error {
	if old.GetPool() == new.GetPool() {
		return nil
	}
	client, err := clusterForPool(old.GetPool())
	if err != nil {
		return err
	}
	newClient, err := clusterForPool(new.GetPool())
	if err != nil {
		return err
	}
	sameCluster := client.GetCluster().Name == newClient.GetCluster().Name
	sameNamespace := client.PoolNamespace(old.GetPool()) == client.PoolNamespace(new.GetPool())
	if sameCluster && !sameNamespace {
		var volumes []volume.Volume
		volumes, err = volume.ListByApp(old.GetName())
		if err != nil {
			return err
		}
		if len(volumes) > 0 {
			return fmt.Errorf("can't change the pool of an app with binded volumes")
		}
	}
	versions, err := versionsForAppProcess(client, old, "")
	if err != nil {
		return err
	}
	params := updatePipelineParams{
		old:      old,
		new:      new,
		w:        w,
		p:        p,
		versions: versions,
	}
	if !sameCluster {
		actions := []*action.Action{
			&provisionNewApp,
			&restartApp,
			&rebuildAppRoutes,
			&destroyOldApp,
		}
		return action.NewPipeline(actions...).Execute(params)
	}
	// same cluster and it is not configured with per-pool-namespace, nothing to do.
	if sameNamespace {
		return nil
	}
	actions := []*action.Action{
		&updateAppCR,
		&restartApp,
		&rebuildAppRoutes,
		&removeOldAppResources,
	}
	return action.NewPipeline(actions...).Execute(params)
}

func (p *kubernetesProvisioner) Shutdown(ctx context.Context) error {
	err := forEachCluster(func(client *ClusterClient) error {
		stopClusterController(p, client)
		return nil
	})
	if err == provTypes.ErrNoCluster {
		return nil
	}
	return err
}

func ensureAppCustomResourceSynced(client *ClusterClient, a provision.App) error {
	_, err := loadAndEnsureAppCustomResourceSynced(client, a)
	return err
}

func loadAndEnsureAppCustomResourceSynced(client *ClusterClient, a provision.App) (*tsuruv1.App, error) {
	err := ensureNamespace(client, client.Namespace())
	if err != nil {
		return nil, err
	}
	err = ensureAppCustomResource(client, a)
	if err != nil {
		return nil, err
	}

	tclient, err := TsuruClientForConfig(client.restConfig)
	if err != nil {
		return nil, err
	}
	appCRD, err := tclient.TsuruV1().Apps(client.Namespace()).Get(a.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	appCRD.Spec.ServiceAccountName = serviceAccountNameForApp(a)

	deploys, err := allDeploymentsForApp(client, a)
	if err != nil {
		return nil, err
	}

	svcs, err := allServicesForApp(client, a)
	if err != nil {
		return nil, err
	}

	deployments := make(map[string][]string)
	services := make(map[string][]string)
	for _, dep := range deploys {
		l := labelSetFromMeta(&dep.ObjectMeta)
		proc := l.AppProcess()
		deployments[proc] = append(deployments[proc], dep.Name)
	}

	for _, svc := range svcs {
		l := labelSetFromMeta(&svc.ObjectMeta)
		proc := l.AppProcess()
		services[proc] = append(services[proc], svc.Name)
	}

	appCRD.Spec.Services = services
	appCRD.Spec.Deployments = deployments

	version, err := servicemanager.AppVersion.LatestSuccessfulVersion(a)
	if err != nil && err != appTypes.ErrNoVersionsAvailable {
		return nil, err
	}

	if version != nil {
		appCRD.Spec.Configs, err = normalizeConfigs(version)
		if err != nil {
			return nil, err
		}
	}

	return tclient.TsuruV1().Apps(client.Namespace()).Update(appCRD)
}

func ensureAppCustomResource(client *ClusterClient, a provision.App) error {
	err := ensureCustomResourceDefinitions(client)
	if err != nil {
		return err
	}
	tclient, err := TsuruClientForConfig(client.restConfig)
	if err != nil {
		return err
	}
	_, err = tclient.TsuruV1().Apps(client.Namespace()).Get(a.GetName(), metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !k8sErrors.IsNotFound(err) {
		return err
	}
	_, err = tclient.TsuruV1().Apps(client.Namespace()).Create(&tsuruv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: a.GetName()},
		Spec:       tsuruv1.AppSpec{NamespaceName: client.PoolNamespace(a.GetPool())},
	})
	return err
}

func ensureCustomResourceDefinitions(client *ClusterClient) error {
	extClient, err := ExtensionsClientForConfig(client.restConfig)
	if err != nil {
		return err
	}
	toCreate := appCustomResourceDefinition()
	_, err = extClient.ApiextensionsV1beta1().CustomResourceDefinitions().Create(toCreate)
	if err != nil && !k8sErrors.IsAlreadyExists(err) {
		return err
	}
	timeout := time.After(time.Minute)
loop:
	for {
		crd, errGet := extClient.ApiextensionsV1beta1().CustomResourceDefinitions().Get(toCreate.GetName(), metav1.GetOptions{})
		if errGet != nil {
			return errGet
		}
		for _, c := range crd.Status.Conditions {
			if c.Type == v1beta1.Established && c.Status == v1beta1.ConditionTrue {
				break loop
			}
		}
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for custom resource definition creation")
		case <-time.After(time.Second):
		}
	}
	return nil
}

func appCustomResourceDefinition() *v1beta1.CustomResourceDefinition {
	return &v1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "apps.tsuru.io"},
		Spec: v1beta1.CustomResourceDefinitionSpec{
			Group:   "tsuru.io",
			Version: "v1",
			Names: v1beta1.CustomResourceDefinitionNames{
				Plural:   "apps",
				Singular: "app",
				Kind:     "App",
				ListKind: "AppList",
			},
		},
	}
}

func normalizeConfigs(version appTypes.AppVersion) (*provTypes.TsuruYamlKubernetesConfig, error) {
	yamlData, err := version.TsuruYamlData()
	if err != nil {
		return nil, err
	}

	config := yamlData.Kubernetes
	if config == nil {
		return nil, nil
	}

	for _, group := range yamlData.Kubernetes.Groups {
		for procName, proc := range group {
			ports, err := getProcessPortsForVersion(version, procName)
			if err == nil {
				proc.Ports = ports
				group[procName] = proc
			}
		}
	}
	return config, nil
}

func EnvsForApp(a provision.App, process string, version appTypes.AppVersion, isDeploy bool) []bind.EnvVar {
	envs := provision.EnvsForApp(a, process, isDeploy, version)
	if isDeploy {
		return envs
	}

	portsConfig, err := getProcessPortsForVersion(version, process)
	if err != nil {
		return envs
	}
	if len(portsConfig) == 0 {
		return removeDefaultPortEnvs(envs)
	}

	portValue := make([]string, len(portsConfig))
	for i, portConfig := range portsConfig {
		targetPort := portConfig.TargetPort
		if targetPort == 0 {
			targetPort = portConfig.Port
		}
		portValue[i] = fmt.Sprintf("%d", targetPort)
	}
	portEnv := bind.EnvVar{Name: fmt.Sprintf("PORT_%s", process), Value: strings.Join(portValue, ",")}
	if !isDefaultPort(portsConfig) {
		envs = removeDefaultPortEnvs(envs)
	}
	return append(envs, portEnv)
}

func removeDefaultPortEnvs(envs []bind.EnvVar) []bind.EnvVar {
	envsWithoutPort := []bind.EnvVar{}
	defaultPortEnvs := provision.DefaultWebPortEnvs()
	for _, env := range envs {
		isDefaultPortEnv := false
		for _, defaultEnv := range defaultPortEnvs {
			if env.Name == defaultEnv.Name {
				isDefaultPortEnv = true
				break
			}
		}
		if !isDefaultPortEnv {
			envsWithoutPort = append(envsWithoutPort, env)
		}
	}

	return envsWithoutPort
}

func isDefaultPort(portsConfig []provTypes.TsuruYamlKubernetesProcessPortConfig) bool {
	if len(portsConfig) != 1 {
		return false
	}

	defaultPort := defaultKubernetesPodPortConfig()
	return portsConfig[0].Protocol == defaultPort.Protocol &&
		portsConfig[0].Port == defaultPort.Port &&
		portsConfig[0].TargetPort == defaultPort.TargetPort
}

func (p *kubernetesProvisioner) HandlesHC() bool {
	return true
}

func (p *kubernetesProvisioner) ToggleRoutable(a provision.App, version appTypes.AppVersion, isRoutable bool) error {
	client, err := clusterForPool(a.GetPool())
	if err != nil {
		return err
	}
	depsData, err := deploymentsDataForApp(client, a)
	if err != nil {
		return err
	}
	depsForVersion, ok := depsData.versioned[version.Version()]
	if !ok {
		return errors.Errorf("no deployment found for version %v", version.Version())
	}
	for _, depData := range depsForVersion {
		err = toggleRoutableDeployment(client, depData.dep, isRoutable)
		if err != nil {
			return err
		}
	}
	return nil
}

func toggleRoutableDeployment(client *ClusterClient, dep *appsv1.Deployment, isRoutable bool) error {
	ls := labelOnlySetFromMeta(&dep.ObjectMeta)
	ls.ToggleIsRoutable(isRoutable)
	dep.Spec.Paused = true
	dep.ObjectMeta.Labels = ls.ToLabels()
	dep.Spec.Template.ObjectMeta.Labels = ls.WithoutAppReplicas().ToLabels()
	_, err := client.AppsV1().Deployments(dep.Namespace).Update(dep)
	if err != nil {
		return errors.WithStack(err)
	}

	rs, err := activeReplicaSetForDeployment(client, dep)
	if err != nil {
		return err
	}
	ls = labelOnlySetFromMeta(&rs.ObjectMeta)
	ls.ToggleIsRoutable(isRoutable)
	rs.ObjectMeta.Labels = ls.ToLabels()
	rs.Spec.Template.ObjectMeta.Labels = ls.ToLabels()
	_, err = client.AppsV1().ReplicaSets(rs.Namespace).Update(rs)
	if err != nil {
		return errors.WithStack(err)
	}

	pods, err := podsForReplicaSet(client, rs)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		ls = labelOnlySetFromMeta(&pod.ObjectMeta)
		ls.ToggleIsRoutable(isRoutable)
		pod.ObjectMeta.Labels = ls.ToLabels()
		_, err = client.CoreV1().Pods(pod.Namespace).Update(&pod)
		if err != nil {
			return errors.WithStack(err)
		}
	}
	return nil
}

func (p *kubernetesProvisioner) DeployedVersions(a provision.App) ([]int, error) {
	client, err := clusterForPool(a.GetPool())
	if err != nil {
		return nil, err
	}
	deps, err := deploymentsDataForApp(client, a)
	if err != nil {
		return nil, err
	}
	var versions []int
	for v := range deps.versioned {
		versions = append(versions, v)
	}
	return versions, nil
}
