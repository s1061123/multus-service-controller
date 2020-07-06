/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// ServiceHandler is an abstract interface of objects which receive
// notifications about pod object changes.
type ServiceHandler interface {
	// OnServiceAdd is called whenever creation of new pod object
	// is observed.
	OnServiceAdd(pod *v1.Service)
	// OnServiceUpdate is called whenever modification of an existing
	// pod object is observed.
	OnServiceUpdate(oldService, pod *v1.Service)
	// OnServiceDelete is called whenever deletion of an existing pod
	// object is observed.
	OnServiceDelete(pod *v1.Service)
	// OnServiceSynced is called once all the initial event handlers were
	// called and the state is fully propagated to local cache.
	OnServiceSynced()
}

// ServiceConfig
type ServiceConfig struct {
	listerSynced cache.InformerSynced
	eventHandlers []ServiceHandler
}

// NewServiceConfig creates a new ServiceConfig.
func NewServiceConfig(svcInformer coreinformers.ServiceInformer, resyncPeriod time.Duration) *ServiceConfig {
	result := &ServiceConfig{
		listerSynced: svcInformer.Informer().HasSynced,
	}

	svcInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: result.handleAddService,
			UpdateFunc: result.handleUpdateService,
			DeleteFunc: result.handleDeleteService,
		},
		resyncPeriod,
	)
	return result
}

// RegisterEventHandler registers a handler which is called on every service change.
func (c *ServiceConfig) RegisterEventHandler(handler ServiceHandler) {
	c.eventHandlers = append(c.eventHandlers, handler)
}

// Run waits for cache synced and invokes handlers after syncing.
func (c *ServiceConfig) Run(stopCh <-chan struct{}) {
	klog.Info("Starting service config controller")

	if !cache.WaitForNamedCacheSync("service config", stopCh, c.listerSynced) {
		return
	}

	for i := range c.eventHandlers {
		klog.V(4).Infof("Calling handler.OnServiceSynced()")
		c.eventHandlers[i].OnServiceSynced()
	}
}

func (c *ServiceConfig) handleAddService(obj interface{}) {
	pod, ok := obj.(*v1.Service)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("unexpected object type: %v", obj))
		return
	}

	for i := range c.eventHandlers {
		klog.V(4).Infof("Calling handler.OnServiceAdd")
		c.eventHandlers[i].OnServiceAdd(pod)
	}
}

func (c *ServiceConfig) handleUpdateService(oldObj, newObj interface{}) {
	oldService, ok := oldObj.(*v1.Service)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("unexpected object type: %v", oldObj))
		return
	}
	pod, ok := newObj.(*v1.Service)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("unexpected object type: %v", newObj))
		return
	}
	for i := range c.eventHandlers {
		klog.V(4).Infof("Calling handler.OnServiceUpdate")
		c.eventHandlers[i].OnServiceUpdate(oldService, pod)
	}
}

func (c *ServiceConfig) handleDeleteService(obj interface{}) {
	pod, ok := obj.(*v1.Service)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("unexpected object type: %v", obj))
		}
		if pod, ok = tombstone.Obj.(*v1.Service); !ok {
			utilruntime.HandleError(fmt.Errorf("unexpected object type: %v", obj))
			return
		}
	}
	for i := range c.eventHandlers {
		klog.V(4).Infof("Calling handler.OnServiceDelete")
		c.eventHandlers[i].OnServiceDelete(pod)
	}
}

// ServiceInfo contains information that defines a service.
type ServiceInfo struct {
	Service *v1.Service
}

// String ...
func (info *ServiceInfo) String() string {
	return fmt.Sprintf("pod:%s", info.Service.Name)
}

type serviceChange struct {
	previous ServiceMap
	current ServiceMap
}

// ServiceChangeTracker carries state about uncommitted changes to an arbitrary number of
// Services in the node, keyed by their namespace and name
type ServiceChangeTracker struct {
	// lock protects items.
	lock          sync.Mutex
	items map[types.NamespacedName]*serviceChange
}

// String
func (sct *ServiceChangeTracker) String() string {
	return fmt.Sprintf("serviceChange: %v", sct.items)
}

func NewServiceChangeTracker() *ServiceChangeTracker {
	return &ServiceChangeTracker{
		items: make(map[types.NamespacedName]*serviceChange),
	}
}

func (sct *ServiceChangeTracker) newServiceInfo(service *v1.Service) (*ServiceInfo, error) {
	info := &ServiceInfo{
		Service: service,
	}
	return info, nil
}

func (sct *ServiceChangeTracker) serviceToServiceMap(service *v1.Service) ServiceMap {
	if service == nil {
		return nil
	}

	serviceMap := make(ServiceMap)
	serviceInfo, err := sct.newServiceInfo(service)
	if err != nil {
		return nil
	}

	serviceMap[types.NamespacedName{Namespace: service.Namespace, Name: service.Name}] = *serviceInfo
	return serviceMap
}

func (sct *ServiceChangeTracker) Update(previous, current *v1.Service) bool {
	service := current

	if sct == nil {
		return false
	}

	if service == nil {
		service = previous
	}
	if service == nil {
		return false
	}
	namespacedName := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}

	sct.lock.Lock()
	defer sct.lock.Unlock()

	change, exists := sct.items[namespacedName]
	if !exists {
		change = &serviceChange{}
		prevServiceMap := sct.serviceToServiceMap(previous)
		change.previous = prevServiceMap
		sct.items[namespacedName] = change
	}

	curServiceMap := sct.serviceToServiceMap(current)
	change.current = curServiceMap
	if reflect.DeepEqual(change.previous, change.current) {
		delete(sct.items, namespacedName)
	}
	return len(sct.items) >= 0
}

// ServiceMap ...
type ServiceMap map[types.NamespacedName]ServiceInfo

// Update updates serviceMap base on the given changes
func (sm *ServiceMap) Update(changes *ServiceChangeTracker) {
	if sm != nil {
		sm.apply(changes)
	}
}

func (sm *ServiceMap) apply(changes *ServiceChangeTracker) {
	if sm == nil || changes == nil {
		return
	}

	changes.lock.Lock()
	defer changes.lock.Unlock()
	for _, change := range changes.items {
		sm.unmerge(change.previous)
		sm.merge(change.current)
	}
	// clear changes after applying them to ServiceMap.
	changes.items = make(map[types.NamespacedName]*serviceChange)
	return
}

func (sm *ServiceMap) merge(other ServiceMap) {
	for serviceName, info := range other {
		(*sm)[serviceName] = info
	}
}

func (sm *ServiceMap) unmerge(other ServiceMap) {
	for serviceName := range other {
		delete(*sm, serviceName)
	}
}
