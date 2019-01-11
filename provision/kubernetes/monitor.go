// Copyright 2016 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kubernetes

import (
	"fmt"
	"sync"

	"github.com/tsuru/tsuru/app"
	"github.com/tsuru/tsuru/log"
	"github.com/tsuru/tsuru/router/rebuild"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	routerReadyAnnotation = "tsuru.io/router-ready"
)

var (
	podMonitors   = map[string]*podMonitor{}
	podMonitorsMu sync.Mutex
)

type podMonitor struct {
	p       *kubernetesProvisioner
	cluster *ClusterClient
}

func startMonitoring(p *kubernetesProvisioner, cluster *ClusterClient) *podMonitor {
	podMonitorsMu.Lock()
	defer podMonitorsMu.Unlock()
	if m, ok := podMonitors[cluster.Name]; ok {
		return m
	}
	m := &podMonitor{
		p:       p,
		cluster: cluster,
	}
	podMonitors[cluster.Name] = m
	m.start()
	return m
}

func (m *podMonitor) start() error {
	informer, err := m.p.podInformerForCluster(m.cluster)
	if err != nil {
		return err
	}
	informer.Informer().AddEventHandler(m)
	return nil
}

func (m *podMonitor) OnAdd(obj interface{}) {

}
func (m *podMonitor) OnUpdate(oldObj, newObj interface{}) {
	pod, ok := newObj.(*apiv1.Pod)
	if !ok {
		return
	}
	_, isSet := pod.ObjectMeta.Annotations[routerReadyAnnotation]
	if isSet {
		return
	}
	if !isPodReady(pod) {
		return
	}
	labelSet := labelSetFromMeta(&pod.ObjectMeta)
	appName := labelSet.AppName()
	if appName == "" {
		return
	}
	podApp, err := app.GetByName(appName)
	if err != nil {
		log.Errorf("unable to find app for pod: %v", err)
		return
	}
	_, err = rebuild.RebuildRoutes(podApp, false)
	if err != nil {
		log.Errorf("unable to rebuild routes: %v", err)
		return
	}
	client, err := clusterForPool(podApp.GetPool())
	if err != nil {
		log.Errorf("unable to get cluster client: %v", err)
		return
	}
	ns, err := client.AppNamespace(podApp)
	if err != nil {
		log.Errorf("unable to get app namespace: %v", err)
		return
	}
	_, err = client.CoreV1().Pods(ns).Patch(pod.Name, types.MergePatchType, []byte(fmt.Sprintf(`{
		"metadata": {"annotations": {%q: "true"}}
	}`, routerReadyAnnotation)))
	if err != nil {
		log.Errorf("unable to patch pod: %v", err)
		return
	}
}

func (m *podMonitor) OnDelete(obj interface{}) {

}
