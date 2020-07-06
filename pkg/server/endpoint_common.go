package server

import (
    "fmt"
    "strings"

    v1 "k8s.io/api/core/v1"
    proxyapis "k8s.io/kubernetes/pkg/proxy/apis"
)

const MultusServiceEndpointsNameAnnotation = "k8s.v1.cni.cncf.io/service-endpoints-name"
const MultusServiceNetworksAnnotation = "k8s.v1.cni.cncf.io/service-networks"
const MultusEndpointsServiceNameAnnotation = "k8s.v1.cni.cncf.io/service-name"

func GetMultusServiceInfo(service *v1.Service) (bool, string, []string) {
	proxyName, ok := service.Labels[proxyapis.LabelServiceProxyName]
	if ! ok {
		return false, "", nil
	}
	if proxyName != "multus-proxy" {
		return false, "", nil
	}

	endpointsName, ok := service.Annotations[MultusServiceEndpointsNameAnnotation]
	if ! ok {
		return false, "", nil
	}

	serviceNetworksAnnot, ok := service.Annotations[MultusServiceNetworksAnnotation]
	if ! ok {
		return false, "", nil
	}
	serviceNetworksAnnot= strings.ReplaceAll(serviceNetworksAnnot, " ", "")
	serviceNetworks := strings.Split(serviceNetworksAnnot, ",")

	for idx, networkName := range serviceNetworks {
		if strings.IndexAny(networkName, "/") == -1 {
			serviceNetworks[idx] = fmt.Sprintf("%s/%s", service.Namespace, networkName)
		}
	}

	return true, endpointsName, serviceNetworks
}

func (s *Server) ServiceCreated(service *v1.Service) {
	if s.endpointSliceUsed {
		s.EndpointSliceServiceCreated(service)
		return
	}
	s.EndpointServiceCreated(service)
	return
}

func (s *Server) ServiceDeleted(service *v1.Service) {
	if s.endpointSliceUsed {
		s.EndpointSliceServiceDeleted(service)
		return
	}
	s.EndpointServiceDeleted(service)
	return
}

func (s *Server) ServiceUpdated(old, current *v1.Service) {
	if s.endpointSliceUsed {
		s.EndpointSliceServiceUpdated(old, current)
		return
	}
	s.EndpointServiceUpdated(old, current)
	return
}
