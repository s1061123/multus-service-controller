package server

import (
    "context"

    v1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/api/errors"
    "k8s.io/apimachinery/pkg/labels"
    "k8s.io/apimachinery/pkg/util/intstr"
    "k8s.io/klog/v2"
)

func (s *Server) EndpointServiceCreated(service *v1.Service) {
	klog.V(4).Infof("Service created")

	isMultusService, endpointsName, _ := GetMultusServiceInfo(service)
	if !isMultusService {
		klog.V(4).Infof("Not multus service!, skipped")
		return
	}

	endpoints, err := s.Client.CoreV1().Endpoints(service.Namespace).Get(context.TODO(), endpointsName, metav1.GetOptions{})
	if err == nil {
		klog.Errorf("endpoints name %s is already used", endpointsName)
		return
	} else if !errors.IsNotFound(err) {
		klog.Errorf("error on get endpoints: %v", err)
		return
	}

	endpoints = &v1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name: endpointsName,
			Labels: service.Labels,
		},
	}

	endpoints, err = s.Client.CoreV1().Endpoints(service.Namespace).Create(context.TODO(), endpoints, metav1.CreateOptions{})
	if err != nil {
		klog.Errorf("error on create endpoints: %v", err)
	}

	//XXX: UpdateEndpoints
	s.UpdateEndpoints(service)
}

func (s *Server) EndpointServiceDeleted(service *v1.Service) {
	klog.V(4).Infof("Endpoint Delete")

	isMultusService, endpointsName, _ := GetMultusServiceInfo(service)
	if !isMultusService {
		klog.V(4).Infof("Not multus service!, skipped")
		return
	}

	//XXX: redundant check?
	_, err := s.Client.CoreV1().Endpoints(service.Namespace).Get(context.TODO(), endpointsName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("error on get endpoints: %v", err)
		return
	}

	err = s.Client.CoreV1().Endpoints(service.Namespace).Delete(context.TODO(), endpointsName, metav1.DeleteOptions{})
	if err != nil {
		klog.Errorf("error on get endpoints: %v", err)
	}
}

func (s *Server) EndpointServiceUpdated(old, current *v1.Service) {
	klog.V(4).Infof("Service Updated")
	isPrevMultusService, oldEndpointsName, _ := GetMultusServiceInfo(old)
	isCurrentMultusService, currentEndpointsName, _ := GetMultusServiceInfo(current)

	if !isPrevMultusService && isCurrentMultusService {
		klog.V(4).Infof("newly added multus service")
		endpoints := &v1.Endpoints{
			ObjectMeta: metav1.ObjectMeta{
				Name: currentEndpointsName,
				Labels: current.Labels,
				Annotations: map[string]string{
					MultusEndpointsServiceNameAnnotation: current.Name,
				},
			},
		}
		endpoints, err := s.Client.CoreV1().Endpoints(current.Namespace).Create(context.TODO(), endpoints, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("error on create endpoints: %v", err)
		}
		s.UpdateEndpoints(current)
	} else if isPrevMultusService && !isCurrentMultusService {
		klog.V(4).Infof("deleted multus service")
		err := s.Client.CoreV1().Endpoints(old.Namespace).Delete(context.TODO(), oldEndpointsName, metav1.DeleteOptions{})
		if err != nil {
			klog.Errorf("error on create endpoints: %v", err)
		}
	}

}

func (s *Server) UpdateEndpoints(service *v1.Service) {
	klog.V(4).Infof("Update Endpoints")
	s.podMap.Update(s.podChanges)
	s.serviceMap.Update(s.serviceChanges)

	isMultusService, endpointsName, serviceNetworks := GetMultusServiceInfo(service)
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
	var epAddresses []v1.EndpointAddress
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
					for _, ip := range i.IPs {
						epAddresses = append(epAddresses, v1.EndpointAddress{
							IP: ip,
						})
					}
				}
			}
		}
	}

	var endpointPorts []v1.EndpointPort
	for _, servicePort := range service.Spec.Ports {
		port := servicePort.Port
		if servicePort.TargetPort == intstr.FromInt(0) || servicePort.TargetPort == intstr.FromString("") {
			port = int32(servicePort.TargetPort.IntValue())
		}
		endpointPorts = append(
			endpointPorts,
			v1.EndpointPort{
				Protocol: servicePort.Protocol,
				Port: port,
			})
	}

	klog.V(5).Infof("XXX: %v", endpointIPs)
	endpoints, err := s.Client.CoreV1().Endpoints(service.Namespace).Get(context.TODO(), endpointsName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		klog.Errorf("error on get endpoints: %v", err)
		return
	}

	endpoints.Subsets = []v1.EndpointSubset{
		v1.EndpointSubset{
			Addresses: epAddresses,
			Ports: endpointPorts,
		},
	}

	endpoints, err = s.Client.CoreV1().Endpoints(service.Namespace).Update(context.TODO(), endpoints, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("error on update endpoints: %v", err)
		return
	}
}
