/*
Copyright 2017 The Kubernetes Authors.

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

package service

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"

	log "github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/kubernetes-incubator/external-dns/endpoint"

	. "github.com/kubernetes-incubator/external-dns/source"
)

const (
	defaultTargetsCapacity = 10
)

// serviceSource is an implementation of Source for Kubernetes service objects.
// It will find all services that are under our jurisdiction, i.e. annotated
// desired hostname and matching or no controller annotation. For each of the
// matched services' entrypoints it will return a corresponding
// Endpoint object.
type serviceSource struct {
	client           corev1client.CoreV1Interface
	namespace        string
	annotationFilter string
	// process Services with legacy annotations
	compatibility         string
	fqdnTemplate          *template.Template
	combineFQDNAnnotation bool
	publishInternal       bool
}

// NewSource creates a new serviceSource with the given config.
func NewSource(client corev1client.CoreV1Interface, namespace, annotationFilter string, fqdnTemplate string, combineFqdnAnnotation bool, compatibility string, publishInternal bool) (*serviceSource, error) {
	var (
		tmpl *template.Template
		err  error
	)
	if fqdnTemplate != "" {
		tmpl, err = template.New("endpoint").Funcs(template.FuncMap{
			"trimPrefix": strings.TrimPrefix,
		}).Parse(fqdnTemplate)
		if err != nil {
			return nil, err
		}
	}

	return &serviceSource{
		client:                client,
		namespace:             namespace,
		annotationFilter:      annotationFilter,
		compatibility:         compatibility,
		fqdnTemplate:          tmpl,
		combineFQDNAnnotation: combineFqdnAnnotation,
		publishInternal:       publishInternal,
	}, nil
}

// Endpoints returns endpoint objects for each service that should be processed.
func (sc *serviceSource) Endpoints() ([]*endpoint.Endpoint, error) {
	services, err := sc.client.Services(sc.namespace).List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	services.Items, err = sc.filterByAnnotations(services.Items)
	if err != nil {
		return nil, err
	}

	endpoints := []*endpoint.Endpoint{}

	for _, svc := range services.Items {
		// Check controller annotation to see if we are responsible.
		controller, ok := svc.Annotations[ControllerAnnotationKey]
		if ok && controller != ControllerAnnotationValue {
			log.Debugf("Skipping service %s/%s because controller value does not match, found: %s, required: %s",
				svc.Namespace, svc.Name, controller, ControllerAnnotationValue)
			continue
		}

		svcEndpoints := sc.endpoints(&svc)

		// process legacy annotations if no endpoints were returned and compatibility mode is enabled.
		if len(svcEndpoints) == 0 && sc.compatibility != "" {
			svcEndpoints = legacyEndpointsFromService(&svc, sc.compatibility)
		}

		// apply template if none of the above is found
		if (sc.combineFQDNAnnotation || len(svcEndpoints) == 0) && sc.fqdnTemplate != nil {
			sEndpoints, err := sc.endpointsFromTemplate(&svc)
			if err != nil {
				return nil, err
			}

			if sc.combineFQDNAnnotation {
				svcEndpoints = append(svcEndpoints, sEndpoints...)
			} else {
				svcEndpoints = sEndpoints
			}
		}

		if len(svcEndpoints) == 0 {
			log.Debugf("No endpoints could be generated from service %s/%s", svc.Namespace, svc.Name)
			continue
		}

		log.Debugf("Endpoints generated from service: %s/%s: %v", svc.Namespace, svc.Name, svcEndpoints)
		sc.setResourceLabel(svc, svcEndpoints)
		endpoints = append(endpoints, svcEndpoints...)
	}

	for _, ep := range endpoints {
		sort.Sort(ep.Targets)
	}

	return endpoints, nil
}

func (sc *serviceSource) extractHeadlessEndpoints(svc *corev1.Service, hostname string) []*endpoint.Endpoint {

	var endpoints []*endpoint.Endpoint

	pods, err := sc.client.Pods(svc.Namespace).List(metav1.ListOptions{LabelSelector: labels.Set(svc.Spec.Selector).AsSelectorPreValidated().String()})

	if err != nil {
		log.Errorf("List Pods of service[%s] error:%v", svc.GetName(), err)
		return endpoints
	}

	for _, v := range pods.Items {
		headlessDomain := hostname
		if v.Spec.Hostname != "" {
			headlessDomain = v.Spec.Hostname + "." + headlessDomain
		}

		log.Debugf("Generating matching endpoint %s with PodIP %s", headlessDomain, v.Status.PodIP)
		// To reduce traffice on the DNS API only add record for running Pods. Good Idea?
		if v.Status.Phase == corev1.PodRunning {
			endpoints = append(endpoints, endpoint.NewEndpoint(headlessDomain, endpoint.RecordTypeA, v.Status.PodIP))
		} else {
			log.Debugf("Pod %s is not in running phase", v.Spec.Hostname)
		}
	}

	return endpoints
}
func (sc *serviceSource) endpointsFromTemplate(svc *corev1.Service) ([]*endpoint.Endpoint, error) {
	var endpoints []*endpoint.Endpoint

	// Process the whole template string
	var buf bytes.Buffer
	err := sc.fqdnTemplate.Execute(&buf, svc)
	if err != nil {
		return nil, fmt.Errorf("failed to apply template on service %s: %v", svc.String(), err)
	}

	hostnameList := strings.Split(strings.Replace(buf.String(), " ", "", -1), ",")
	for _, hostname := range hostnameList {
		endpoints = append(endpoints, sc.generateEndpoints(svc, hostname)...)
	}

	return endpoints, nil
}

// endpointsFromService extracts the endpoints from a service object
func (sc *serviceSource) endpoints(svc *corev1.Service) []*endpoint.Endpoint {
	var endpoints []*endpoint.Endpoint

	// Get the desired hostname of the service from the annotation.
	hostnameAnnotation, exists := svc.Annotations[HostnameAnnotationKey]
	if !exists {
		return nil
	}

	hostnameList := strings.Split(strings.Replace(hostnameAnnotation, " ", "", -1), ",")
	for _, hostname := range hostnameList {
		endpoints = append(endpoints, sc.generateEndpoints(svc, hostname)...)
	}

	return endpoints
}

// filterByAnnotations filters a list of services by a given annotation selector.
func (sc *serviceSource) filterByAnnotations(services []corev1.Service) ([]corev1.Service, error) {
	labelSelector, err := metav1.ParseToLabelSelector(sc.annotationFilter)
	if err != nil {
		return nil, err
	}
	selector, err := metav1.LabelSelectorAsSelector(labelSelector)
	if err != nil {
		return nil, err
	}

	// empty filter returns original list
	if selector.Empty() {
		return services, nil
	}

	filteredList := []corev1.Service{}

	for _, service := range services {
		// convert the service's annotations to an equivalent label selector
		annotations := labels.Set(service.Annotations)

		// include service if its annotations match the selector
		if selector.Matches(annotations) {
			filteredList = append(filteredList, service)
		}
	}

	return filteredList, nil
}

func (sc *serviceSource) setResourceLabel(service corev1.Service, endpoints []*endpoint.Endpoint) {
	for _, ep := range endpoints {
		ep.Labels[endpoint.ResourceLabelKey] = fmt.Sprintf("service/%s/%s", service.Namespace, service.Name)
	}
}

func (sc *serviceSource) generateEndpoints(svc *corev1.Service, hostname string) []*endpoint.Endpoint {
	hostname = strings.TrimSuffix(hostname, ".")
	ttl, err := GetTTLFromAnnotations(svc.Annotations)
	if err != nil {
		log.Warn(err)
	}

	epA := &endpoint.Endpoint{
		RecordTTL:  ttl,
		RecordType: endpoint.RecordTypeA,
		Labels:     endpoint.NewLabels(),
		Targets:    make(endpoint.Targets, 0, defaultTargetsCapacity),
		DNSName:    hostname,
	}

	epCNAME := &endpoint.Endpoint{
		RecordTTL:  ttl,
		RecordType: endpoint.RecordTypeCNAME,
		Labels:     endpoint.NewLabels(),
		Targets:    make(endpoint.Targets, 0, defaultTargetsCapacity),
		DNSName:    hostname,
	}

	var endpoints []*endpoint.Endpoint
	var targets endpoint.Targets

	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		targets = append(targets, extractLoadBalancerTargets(svc)...)
	case corev1.ServiceTypeClusterIP:
		if sc.publishInternal {
			targets = append(targets, extractServiceIps(svc)...)
		}
		if svc.Spec.ClusterIP == corev1.ClusterIPNone {
			endpoints = append(endpoints, sc.extractHeadlessEndpoints(svc, hostname)...)
		}

	}

	for _, t := range targets {
		if SuitableType(t) == endpoint.RecordTypeA {
			epA.Targets = append(epA.Targets, t)
		}
		if SuitableType(t) == endpoint.RecordTypeCNAME {
			epCNAME.Targets = append(epCNAME.Targets, t)
		}
	}

	if len(epA.Targets) > 0 {
		endpoints = append(endpoints, epA)
	}
	if len(epCNAME.Targets) > 0 {
		endpoints = append(endpoints, epCNAME)
	}
	return endpoints
}

func extractServiceIps(svc *corev1.Service) endpoint.Targets {
	if svc.Spec.ClusterIP == corev1.ClusterIPNone {
		log.Debugf("Unable to associate %s headless service with a Cluster IP", svc.Name)
		return endpoint.Targets{}
	}
	return endpoint.Targets{svc.Spec.ClusterIP}
}

func extractLoadBalancerTargets(svc *corev1.Service) endpoint.Targets {
	var targets endpoint.Targets

	// Create a corresponding endpoint for each configured external entrypoint.
	for _, lb := range svc.Status.LoadBalancer.Ingress {
		if lb.IP != "" {
			targets = append(targets, lb.IP)
		}
		if lb.Hostname != "" {
			targets = append(targets, lb.Hostname)
		}
	}

	return targets
}
