package server

import (
    "sync"
    "sync/atomic"
    "time"

    "github.com/s1061123/multus-service-controller/pkg/controllers"

    v1 "k8s.io/api/core/v1"
    //metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/labels"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/apimachinery/pkg/util/wait"
    "k8s.io/client-go/informers"
    clientset "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/kubernetes/scheme"
    v1core "k8s.io/client-go/kubernetes/typed/core/v1"
    v1beta1 "k8s.io/client-go/listers/discovery/v1beta1"
    corelisters "k8s.io/client-go/listers/core/v1"
    "k8s.io/client-go/rest"
    "k8s.io/client-go/tools/clientcmd"
    clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
    "k8s.io/client-go/tools/record"
    "k8s.io/klog/v2"
    api "k8s.io/kubernetes/pkg/apis/core"
    "k8s.io/kubernetes/pkg/util/async"
    utilnode "k8s.io/kubernetes/pkg/util/node"
    //"k8s.io/utils/exec"
)

type Server struct {
	podChanges          *controllers.PodChangeTracker
	serviceChanges      *controllers.ServiceChangeTracker
	mu                  sync.Mutex // protects the following fields

	podMap              controllers.PodMap
	serviceMap          controllers.ServiceMap
	Client              clientset.Interface

	Broadcaster         record.EventBroadcaster
	Recorder            record.EventRecorder
	Options             *Options
	ConfigSyncPeriod    time.Duration
	NodeRef             *v1.ObjectReference

	initialized int32
	podSynced bool
	serviceSynced bool
	endpointSliceUsed bool

	podLister corelisters.PodLister
	serviceLister corelisters.ServiceLister
	endpointSliceLister v1beta1.EndpointSliceLister

	syncRunner *async.BoundedFrequencyRunner
}

func (s *Server) Run() error {

	if s.Broadcaster != nil {
		s.Broadcaster.StartRecordingToSink(
			&v1core.EventSinkImpl{Interface: s.Client.CoreV1().Events("")})
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(s.Client, s.ConfigSyncPeriod)
	serviceConfig := controllers.NewServiceConfig(informerFactory.Core().V1().Services(), s.ConfigSyncPeriod)
	serviceConfig.RegisterEventHandler(s)
	go serviceConfig.Run(wait.NeverStop)

	podConfig := controllers.NewPodConfig(informerFactory.Core().V1().Pods(), s.ConfigSyncPeriod)
	podConfig.RegisterEventHandler(s)
	go podConfig.Run(wait.NeverStop)
	s.podLister = informerFactory.Core().V1().Pods().Lister()
	s.serviceLister = informerFactory.Core().V1().Services().Lister()


	informerFactory.Start(wait.NeverStop)

	s.birthCry()
	return nil
}

func (s *Server) setInitialized(value bool) {
	var initialized int32
	if value {
		initialized = 1
	}
	atomic.StoreInt32(&s.initialized, initialized)
}

func (s *Server) isInitialized() bool {
    return atomic.LoadInt32(&s.initialized) > 0
}

func (s *Server) birthCry() {
    klog.Infof("Starting multus-service-controller")
    s.Recorder.Eventf(s.NodeRef, api.EventTypeNormal, "Starting", "Starting multus-service-controller.")
}

// 
func (s *Server) SyncLoop() {
	s.syncRunner.Loop(wait.NeverStop)
}

func NewServer(o *Options) (*Server, error) {
	var kubeConfig *rest.Config
	var err error
	if len(o.Kubeconfig) == 0 {
		klog.Info("Neither kubeconfig file nor master URL was specified. Falling back to in-cluster config.")
		kubeConfig, err = rest.InClusterConfig()
	} else {
		kubeConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: o.Kubeconfig},
			&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: o.master}},
		).ClientConfig()
	}
	if err != nil {
		return nil, err
	}

	client, err := clientset.NewForConfig(kubeConfig)
	if err != nil {
		return nil, err
	}

	hostname, err := utilnode.GetHostname(o.hostnameOverride)
	if err != nil {
		return nil, err
	}

	eventBroadcaster := record.NewBroadcaster()
	recorder := eventBroadcaster.NewRecorder(
		scheme.Scheme,
		v1.EventSource{Component: "macvlan-networkpolicy-node", Host: hostname})

	nodeRef := &v1.ObjectReference{
		Kind:      "Node",
		Name:      hostname,
		UID:       types.UID(hostname),
		Namespace: "",
	}

	syncPeriod := 30 * time.Second
	minSyncPeriod := 0 * time.Second
	burstSyncs := 2

	server := &Server{
		Options: o,
		Client: client,
		Broadcaster: eventBroadcaster,
		Recorder: recorder,
		ConfigSyncPeriod: 15 * time.Minute,
		NodeRef: nodeRef,

		podChanges: controllers.NewPodChangeTracker(),
		serviceChanges: controllers.NewServiceChangeTracker(),
		podMap: make(controllers.PodMap),
		serviceMap: make(controllers.ServiceMap),
	}
	server.syncRunner = async.NewBoundedFrequencyRunner(
		"sync-runner", server.syncEndpoints, minSyncPeriod, syncPeriod, burstSyncs)
	return server, nil
}

// OnPodAdd ...
func (s *Server) OnPodAdd(pod *v1.Pod) {
    klog.V(4).Infof("OnPodUpdate")
    s.OnPodUpdate(nil, pod)
}

// OnPodDelete ...
func (s *Server) OnPodDelete(pod *v1.Pod) {
    klog.V(4).Infof("OnPodDelete")
    s.OnPodUpdate(pod, nil)
}

// OnPodUpdate ...
func (s *Server) OnPodUpdate(oldPod, pod *v1.Pod) {
    klog.V(4).Infof("OnPodUpdate")
    if s.podChanges.Update(oldPod, pod) && s.podSynced {
	    if pod == nil {
		    services, err := s.serviceLister.Services(oldPod.Namespace).List(labels.Everything())
		    if err != nil {
			    klog.Errorf("cannot list services: %v", err)
			    return
		    }
		    for _, svc := range services {
			    svcSelector := labels.Set(svc.Spec.Selector).AsSelectorPreValidated()
			    if svcSelector.Matches(labels.Set(oldPod.Labels)) {
				    s.UpdateEndpoints(svc)
			    }
		    }
	    } else {
		    services, err := s.serviceLister.Services(pod.Namespace).List(labels.Everything())
		    if err != nil {
			    klog.Errorf("cannot list services: %v", err)
			    return
		    }
		    for _, svc := range services {
			    svcSelector := labels.Set(svc.Spec.Selector).AsSelectorPreValidated()
			    if svcSelector.Matches(labels.Set(pod.Labels)) {
				    s.UpdateEndpoints(svc)
			    }
		    }
		    //s.Sync()
	    }
    }
}

// OnPodSynced ...
func (s *Server) OnPodSynced() {
    klog.Infof("OnPodSynced")
    s.mu.Lock()
    s.podSynced = true
    s.setInitialized(s.podSynced)
    s.mu.Unlock()
}


// OnServiceAdd ...
func (s *Server) OnServiceAdd(service *v1.Service) {
    klog.V(4).Infof("OnServiceUpdate")
    if s.serviceChanges.Update(nil, service) && s.serviceSynced {
	    s.ServiceCreated(service)
    }
}

// OnServiceUpdate ...
func (s *Server) OnServiceUpdate(oldService, service *v1.Service) {
    klog.V(4).Infof("OnServiceUpdate")
    if s.serviceChanges.Update(oldService, service) && s.serviceSynced {
	    // XXX: need to check the change of ep name annotation
	    s.ServiceUpdated(oldService, service)
    }
}

// OnServiceDelete ...
func (s *Server) OnServiceDelete(service *v1.Service) {
    klog.V(4).Infof("OnServiceDelete")
    if s.serviceChanges.Update(service, nil) && s.serviceSynced {
	    s.ServiceDeleted(service)
    }
}

// OnServiceSynced ...
func (s *Server) OnServiceSynced() {
    klog.Infof("OnServiceSynced")
    s.mu.Lock()
    s.serviceSynced = true
    s.setInitialized(s.serviceSynced)
    s.mu.Unlock()

    //s.syncMacvlanPolicy()
}

/*
func (s *Server) Sync() {
	klog.Infof("Sync done!")
	s.syncRunner.Run()
}
*/

func (s *Server) syncEndpoints() {
}
