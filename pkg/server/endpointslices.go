package server

import (
    "context"

    v1 "k8s.io/api/core/v1"
    v1beta1 "k8s.io/api/discovery/v1beta1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/api/errors"
    "k8s.io/apimachinery/pkg/labels"
    "k8s.io/apimachinery/pkg/util/intstr"
    "k8s.io/klog/v2"
)

func (s *Server) EndpointSliceServiceCreated(service *v1.Service) {
	klog.V(4).Infof("Service created")

	isMultusService, endpointSlicesName, _ := GetMultusServiceInfo(service)
	if !isMultusService {
		klog.V(4).Infof("Not multus service!, skipped")
		return
	}

	endpointSlices, err := s.Client.DiscoveryV1beta1().EndpointSlices(service.Namespace).Get(context.TODO(), endpointSlicesName, metav1.GetOptions{})
	if err == nil {
		klog.Errorf("endpointslice name %s is already used", endpointSlicesName)
		return
	} else if !errors.IsNotFound(err) {
		klog.Errorf("error on get endpoints: %v", err)
		return
	}

	endpointSlices = &v1beta1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: endpointSlicesName,
			Labels: service.Labels,
		},
	}

	endpointSlices, err = s.Client.DiscoveryV1beta1().EndpointSlices(service.Namespace).Create(context.TODO(), endpointSlices, metav1.CreateOptions{})
	if err != nil {
		klog.Errorf("error on create endpointslices: %v", err)
	}

	s.UpdateEndpointSlices(service)
}

func (s *Server) EndpointSliceServiceDeleted(service *v1.Service) {
	klog.V(4).Infof("EndpointSlices Delete")

	isMultusService, endpointSlicesName, _ := GetMultusServiceInfo(service)
	if !isMultusService {
		klog.V(4).Infof("Not multus service!, skipped")
		return
	}

	//XXX: redundant check?
	_, err := s.Client.DiscoveryV1beta1().EndpointSlices(service.Namespace).Get(context.TODO(), endpointSlicesName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("error on get endpointslices: %v", err)
		return
	}

	err = s.Client.DiscoveryV1beta1().EndpointSlices(service.Namespace).Delete(context.TODO(), endpointSlicesName, metav1.DeleteOptions{})
	if err != nil {
		klog.Errorf("error on get endpointslices: %v", err)
	}
}

func (s *Server) EndpointSliceServiceUpdated(old, current *v1.Service) {
	klog.V(4).Infof("Service Updated")
	isPrevMultusService, oldEndpointSlicesName, _ := GetMultusServiceInfo(old)
	isCurrentMultusService, currentEndpointSlicesName, _ := GetMultusServiceInfo(current)

	if !isPrevMultusService && isCurrentMultusService {
		klog.V(4).Infof("newly added multus service")
		//XXX
		endpoints := &v1beta1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name: currentEndpointSlicesName,
				Labels: current.Labels,
				Annotations: map[string]string{
					MultusEndpointsServiceNameAnnotation: current.Name,
				},
			},
		}
		endpoints, err := s.Client.DiscoveryV1beta1().EndpointSlices(current.Namespace).Create(context.TODO(), endpoints, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("error on create endpointslices: %v", err)
		}
		s.UpdateEndpoints(current)
	} else if isPrevMultusService && !isCurrentMultusService {
		klog.V(4).Infof("deleted multus service")
		err := s.Client.DiscoveryV1beta1().EndpointSlices(old.Namespace).Delete(context.TODO(), oldEndpointSlicesName, metav1.DeleteOptions{})
		if err != nil {
			klog.Errorf("error on create endpointslices: %v", err)
		}
	}

}

func (s *Server) UpdateEndpointSlices(service *v1.Service) {
	klog.V(4).Infof("Update EndpointSlices")
	s.podMap.Update(s.podChanges)
	s.serviceMap.Update(s.serviceChanges)

	isMultusService, endpointSlicesName, serviceNetworks := GetMultusServiceInfo(service)
	if !isMultusService {
		return
	}

	podLabelSelector := labels.Set(service.Spec.Selector).AsSelectorPreValidated()
	pods, err := s.podLister.Pods(service.Namespace).List(podLabelSelector)
	if err != nil {
		klog.Errorf("pod list failed: %v", err)
		return
	}

	if pods == nil {
		return
	}

	var endpointIPs []string //XXX
	var epsEndpoints []v1beta1.Endpoint
	for _, pod := range pods {
		podInfo, err := s.podMap.GetPodInfo(pod)
		if err != nil {
			klog.V(5).Infof("XXX ERR:%v", err)
			continue
		}
		klog.V(5).Infof("XXX: Pod: %s", podInfo.Name)
		for _, i := range podInfo.PodInterfaces {
			for _, networkName := range serviceNetworks {
				if networkName == i.NetattachName {
					klog.V(5).Infof("\tXXX: IF: %s: %s: %v", i.NetattachName, i.InterfaceName, i.IPs)
					endpointIPs = append(endpointIPs, i.IPs...) //XXX
					epsEndpoints = append(epsEndpoints, v1beta1.Endpoint{
						Addresses: i.IPs,
					})
				}
			}
		}
	}

	var endpointPorts []v1beta1.EndpointPort
	for _, servicePort := range service.Spec.Ports {
		port := servicePort.Port
		if servicePort.TargetPort == intstr.FromInt(0) || servicePort.TargetPort == intstr.FromString("") {
			port = int32(servicePort.TargetPort.IntValue())
		}
		endpointPorts = append(
			endpointPorts,
			v1beta1.EndpointPort{
				Protocol: &servicePort.Protocol,
				Port: &port,
			})
	}

	klog.V(5).Infof("XXX: %v", endpointIPs)
	endpointslice, err := s.Client.DiscoveryV1beta1().EndpointSlices(service.Namespace).Get(context.TODO(), endpointSlicesName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		klog.Errorf("error on get endpoints: %v", err)
		return
	}

	endpointslice.Endpoints = epsEndpoints

	endpointslice, err = s.Client.DiscoveryV1beta1().EndpointSlices(service.Namespace).Update(context.TODO(), endpointslice, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("error on update endpoints: %v", err)
		return
	}
}
