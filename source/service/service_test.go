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
	"net"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubernetes-incubator/external-dns/endpoint"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	. "github.com/kubernetes-incubator/external-dns/source"
)

type ServiceSuite struct {
	suite.Suite
	sc             Source
	fooWithTargets *corev1.Service
}

func (suite *ServiceSuite) SetupTest() {
	fakeClient := fake.NewSimpleClientset()
	var err error

	suite.sc, err = NewSource(
		fakeClient.CoreV1(),
		"",
		"",
		"{{.Name}}",
		false,
		"",
		false,
	)
	suite.fooWithTargets = &corev1.Service{
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "foo-with-targets",
			Annotations: map[string]string{},
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{IP: "8.8.8.8"},
					{Hostname: "foo"},
				},
			},
		},
	}

	suite.NoError(err, "should initialize service source")

	_, err = fakeClient.CoreV1().Services(suite.fooWithTargets.Namespace).Create(suite.fooWithTargets)
	suite.NoError(err, "should successfully create service")

}

func (suite *ServiceSuite) TestResourceLabelIsSet() {
	endpoints, _ := suite.sc.Endpoints()
	for _, ep := range endpoints {
		suite.Equal("service/default/foo-with-targets", ep.Labels[endpoint.ResourceLabelKey], "should set correct resource label")
	}
}

func TestServiceSource(t *testing.T) {
	suite.Run(t, new(ServiceSuite))
	t.Run("Interface", testServiceSourceImplementsSource)
	t.Run("NewSource", testServiceSourceNewServiceSource)
	t.Run("Endpoints", testServiceSourceEndpoints)
}

// testServiceSourceImplementsSource tests that serviceSource is a valid Source.
func testServiceSourceImplementsSource(t *testing.T) {
	assert.Implements(t, (*Source)(nil), new(serviceSource))
}

// testServiceSourceNewServiceSource tests that NewSource doesn't return an error.
func testServiceSourceNewServiceSource(t *testing.T) {
	for _, ti := range []struct {
		title            string
		annotationFilter string
		fqdnTemplate     string
		expectError      bool
	}{
		{
			title:        "invalid template",
			expectError:  true,
			fqdnTemplate: "{{.Name",
		},
		{
			title:       "valid empty template",
			expectError: false,
		},
		{
			title:        "valid template",
			expectError:  false,
			fqdnTemplate: "{{.Name}}-{{.Namespace}}.ext-dns.test.com",
		},
		{
			title:            "non-empty annotation filter label",
			expectError:      false,
			annotationFilter: "kubernetes.io/ingress.class=nginx",
		},
	} {
		t.Run(ti.title, func(t *testing.T) {
			_, err := NewSource(
				fake.NewSimpleClientset().CoreV1(),
				"",
				ti.annotationFilter,
				ti.fqdnTemplate,
				false,
				"",
				false,
			)

			if ti.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// testServiceSourceEndpoints tests that various services generate the correct endpoints.
func testServiceSourceEndpoints(t *testing.T) {
	for _, tc := range []struct {
		title                    string
		targetNamespace          string
		annotationFilter         string
		svcNamespace             string
		svcName                  string
		svcType                  corev1.ServiceType
		compatibility            string
		fqdnTemplate             string
		combineFQDNAndAnnotation bool
		labels                   map[string]string
		annotations              map[string]string
		clusterIP                string
		lbs                      []string
		expected                 []*endpoint.Endpoint
		expectError              bool
	}{
		{
			"no annotated services return no endpoints",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{},
			false,
		},
		{
			"annotated services return an endpoint with target IP",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"annotated ClusterIp aren't processed without explicit authorization",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeClusterIP,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"1.2.3.4",
			[]string{},
			[]*endpoint.Endpoint{},
			false,
		},
		{
			"FQDN template with multiple hostnames return an endpoint with target IP",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"{{.Name}}.fqdn.org,{{.Name}}.fqdn.com",
			false,
			map[string]string{},
			map[string]string{},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.fqdn.org", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "foo.fqdn.com", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"FQDN template and annotation both with multiple hostnames return an endpoint with target IP",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"{{.Name}}.fqdn.org,{{.Name}}.fqdn.com",
			true,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org., bar.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "bar.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "foo.fqdn.org", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "foo.fqdn.com", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"annotated services with multiple hostnames return an endpoint with target IP",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org., bar.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "bar.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"annotated services with multiple hostnames and without trailing period return an endpoint with target IP",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org, bar.example.org",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "bar.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"annotated services return an endpoint with target hostname",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"",
			[]string{"lb.example.com"}, // Kubernetes omits the trailing dot
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"lb.example.com"}},
			},
			false,
		},
		{
			"annotated services can omit trailing dot",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org", // Trailing dot is omitted
			},
			"",
			[]string{"1.2.3.4", "lb.example.com"}, // Kubernetes omits the trailing dot
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"lb.example.com"}},
			},
			false,
		},
		{
			"our controller type is dns-controller",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				ControllerAnnotationKey: ControllerAnnotationValue,
				HostnameAnnotationKey:   "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"different controller types are ignored even (with template specified)",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"{{.Name}}.ext-dns.test.com",
			false,
			map[string]string{},
			map[string]string{
				ControllerAnnotationKey: "some-other-tool",
				HostnameAnnotationKey:   "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{},
			false,
		},
		{
			"services are found in target namespace",
			"testing",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"services that are not in target namespace are ignored",
			"testing",
			"",
			"other-testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{},
			false,
		},
		{
			"services are found in all namespaces",
			"",
			"",
			"other-testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"valid matching annotation filter expression",
			"",
			"service.beta.kubernetes.io/external-traffic in (Global, OnlyLocal)",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey:                         "foo.example.org.",
				"service.beta.kubernetes.io/external-traffic": "OnlyLocal",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"valid non-matching annotation filter expression",
			"",
			"service.beta.kubernetes.io/external-traffic in (Global, OnlyLocal)",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey:                         "foo.example.org.",
				"service.beta.kubernetes.io/external-traffic": "SomethingElse",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{},
			false,
		},
		{
			"invalid annotation filter expression",
			"",
			"service.beta.kubernetes.io/external-traffic in (Global OnlyLocal)",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey:                         "foo.example.org.",
				"service.beta.kubernetes.io/external-traffic": "OnlyLocal",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{},
			true,
		},
		{
			"valid matching annotation filter label",
			"",
			"service.beta.kubernetes.io/external-traffic=Global",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey:                         "foo.example.org.",
				"service.beta.kubernetes.io/external-traffic": "Global",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"valid non-matching annotation filter label",
			"",
			"service.beta.kubernetes.io/external-traffic=Global",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey:                         "foo.example.org.",
				"service.beta.kubernetes.io/external-traffic": "OnlyLocal",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{},
			false,
		},
		{
			"no external entrypoints return no endpoints",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"",
			[]string{},
			[]*endpoint.Endpoint{},
			false,
		},
		{
			"multiple external entrypoints return a single endpoint with multiple targets",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4", "8.8.8.8"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4", "8.8.8.8"}},
			},
			false,
		},
		{
			"services annotated with legacy mate annotations are ignored in default mode",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				"zalando.org/dnsname": "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{},
			false,
		},
		{
			"services annotated with legacy mate annotations return an endpoint in compatibility mode",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"mate",
			"",
			false,
			map[string]string{},
			map[string]string{
				"zalando.org/dnsname": "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"services annotated with legacy molecule annotations return an endpoint in compatibility mode",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"molecule",
			"",
			false,
			map[string]string{
				"dns": "route53",
			},
			map[string]string{
				"domainName": "foo.example.org., bar.example.org",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "bar.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"not annotated services with set fqdnTemplate return an endpoint with target IP",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"{{.Name}}.bar.example.com",
			false,
			map[string]string{},
			map[string]string{},
			"",
			[]string{"1.2.3.4", "elb.com"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.bar.example.com", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "foo.bar.example.com", Targets: endpoint.Targets{"elb.com"}},
			},
			false,
		},
		{
			"annotated services with set fqdnTemplate annotation takes precedence",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"{{.Name}}.bar.example.com",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4", "elb.com"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"elb.com"}},
			},
			false,
		},
		{
			"compatibility annotated services with tmpl. compatibility takes precedence",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"mate",
			"{{.Name}}.bar.example.com",
			false,
			map[string]string{},
			map[string]string{
				"zalando.org/dnsname": "mate.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "mate.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"not annotated services with unknown tmpl field should not return anything",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"{{.Calibre}}.bar.example.com",
			false,
			map[string]string{},
			map[string]string{},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{},
			true,
		},
		{
			"ttl not annotated should have RecordTTL.IsConfigured set to false",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}, RecordTTL: endpoint.TTL(0)},
			},
			false,
		},
		{
			"ttl annotated but invalid should have RecordTTL.IsConfigured set to false",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
				TTLAnnotationKey:      "foo",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}, RecordTTL: endpoint.TTL(0)},
			},
			false,
		},
		{
			"ttl annotated and is valid should set Record.TTL",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
				TTLAnnotationKey:      "10",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}, RecordTTL: endpoint.TTL(10)},
			},
			false,
		},
		{
			"Negative ttl is not valid",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeLoadBalancer,
			"",
			"",
			false,
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
				TTLAnnotationKey:      "-10",
			},
			"",
			[]string{"1.2.3.4"},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}, RecordTTL: endpoint.TTL(0)},
			},
			false,
		},
	} {
		t.Run(tc.title, func(t *testing.T) {
			// Create a Kubernetes testing client
			kubernetes := fake.NewSimpleClientset()

			// Create a service to test against
			ingresses := []corev1.LoadBalancerIngress{}
			for _, lb := range tc.lbs {
				if net.ParseIP(lb) != nil {
					ingresses = append(ingresses, corev1.LoadBalancerIngress{IP: lb})
				} else {
					ingresses = append(ingresses, corev1.LoadBalancerIngress{Hostname: lb})
				}
			}

			service := &corev1.Service{
				Spec: corev1.ServiceSpec{
					Type:      tc.svcType,
					ClusterIP: tc.clusterIP,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   tc.svcNamespace,
					Name:        tc.svcName,
					Labels:      tc.labels,
					Annotations: tc.annotations,
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: ingresses,
					},
				},
			}

			_, err := kubernetes.CoreV1().Services(service.Namespace).Create(service)
			require.NoError(t, err)

			// Create our object under test and get the endpoints.
			client, _ := NewSource(
				kubernetes.CoreV1(),
				tc.targetNamespace,
				tc.annotationFilter,
				tc.fqdnTemplate,
				tc.combineFQDNAndAnnotation,
				tc.compatibility,
				false,
			)
			require.NoError(t, err)

			endpoints, err := client.Endpoints()
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			// Validate returned endpoints against desired endpoints.
			ValidateEndpoints(t, endpoints, tc.expected)
		})
	}
}

// testServiceSourceEndpoints tests that various services generate the correct endpoints.
func TestClusterIpServices(t *testing.T) {
	for _, tc := range []struct {
		title            string
		targetNamespace  string
		annotationFilter string
		svcNamespace     string
		svcName          string
		svcType          corev1.ServiceType
		compatibility    string
		fqdnTemplate     string
		labels           map[string]string
		annotations      map[string]string
		clusterIP        string
		lbs              []string
		expected         []*endpoint.Endpoint
		expectError      bool
	}{
		{
			"annotated ClusterIp services return an endpoint with Cluster IP",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeClusterIP,
			"",
			"",
			map[string]string{},
			map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
			"1.2.3.4",
			[]string{},
			[]*endpoint.Endpoint{
				{DNSName: "foo.example.org", Targets: endpoint.Targets{"1.2.3.4"}},
			},
			false,
		},
		{
			"non-annotated ClusterIp services with set fqdnTemplate return an endpoint with target IP",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeClusterIP,
			"",
			"{{.Name}}.bar.example.com",
			map[string]string{},
			map[string]string{},
			"4.5.6.7",
			[]string{},
			[]*endpoint.Endpoint{
				{DNSName: "foo.bar.example.com", Targets: endpoint.Targets{"4.5.6.7"}},
			},
			false,
		},
		{
			"Headless services do not generate endpoints",
			"",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeClusterIP,
			"",
			"",
			map[string]string{},
			map[string]string{},
			corev1.ClusterIPNone,
			[]string{},
			[]*endpoint.Endpoint{},
			false,
		},
	} {
		t.Run(tc.title, func(t *testing.T) {
			// Create a Kubernetes testing client
			kubernetes := fake.NewSimpleClientset()

			// Create a service to test against
			ingresses := []corev1.LoadBalancerIngress{}
			for _, lb := range tc.lbs {
				if net.ParseIP(lb) != nil {
					ingresses = append(ingresses, corev1.LoadBalancerIngress{IP: lb})
				} else {
					ingresses = append(ingresses, corev1.LoadBalancerIngress{Hostname: lb})
				}
			}

			service := &corev1.Service{
				Spec: corev1.ServiceSpec{
					Type:      tc.svcType,
					ClusterIP: tc.clusterIP,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   tc.svcNamespace,
					Name:        tc.svcName,
					Labels:      tc.labels,
					Annotations: tc.annotations,
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: ingresses,
					},
				},
			}

			_, err := kubernetes.CoreV1().Services(service.Namespace).Create(service)
			require.NoError(t, err)

			// Create our object under test and get the endpoints.
			client, _ := NewSource(
				kubernetes.CoreV1(),
				tc.targetNamespace,
				tc.annotationFilter,
				tc.fqdnTemplate,
				false,
				tc.compatibility,
				true,
			)
			require.NoError(t, err)

			endpoints, err := client.Endpoints()
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			// Validate returned endpoints against desired endpoints.
			ValidateEndpoints(t, endpoints, tc.expected)
		})
	}
}

// TestHeadlessServices tests that headless services generate the correct endpoints.
func TestHeadlessServices(t *testing.T) {
	for _, tc := range []struct {
		title           string
		targetNamespace string
		svcNamespace    string
		svcName         string
		svcType         corev1.ServiceType
		compatibility   string
		fqdnTemplate    string
		labels          map[string]string
		annotations     map[string]string
		clusterIP       string
		podIP           string
		selector        map[string]string
		lbs             []string
		podnames        []string
		hostnames       []string
		phases          []corev1.PodPhase
		expected        []*endpoint.Endpoint
		expectError     bool
	}{
		{
			"annotated Headless services return endpoints for each selected Pod",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeClusterIP,
			"",
			"",
			map[string]string{"component": "foo"},
			map[string]string{
				HostnameAnnotationKey: "service.example.org",
			},
			corev1.ClusterIPNone,
			"1.1.1.1",
			map[string]string{
				"component": "foo",
			},
			[]string{},
			[]string{"foo-0", "foo-1"},
			[]string{"foo-0", "foo-1"},
			[]corev1.PodPhase{corev1.PodRunning, corev1.PodRunning},
			[]*endpoint.Endpoint{
				{DNSName: "foo-0.service.example.org", Targets: endpoint.Targets{"1.1.1.1"}},
				{DNSName: "foo-1.service.example.org", Targets: endpoint.Targets{"1.1.1.1"}},
			},
			false,
		},
		{
			"annotated Headless services return endpoints for each selected Pod, which are in running state",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeClusterIP,
			"",
			"",
			map[string]string{"component": "foo"},
			map[string]string{
				HostnameAnnotationKey: "service.example.org",
			},
			corev1.ClusterIPNone,
			"1.1.1.1",
			map[string]string{
				"component": "foo",
			},
			[]string{},
			[]string{"foo-0", "foo-1"},
			[]string{"foo-0", "foo-1"},
			[]corev1.PodPhase{corev1.PodRunning, corev1.PodFailed},
			[]*endpoint.Endpoint{
				{DNSName: "foo-0.service.example.org", Targets: endpoint.Targets{"1.1.1.1"}},
			},
			false,
		},
		{
			"annotated Headless services return endpoints for pods missing hostname",
			"",
			"testing",
			"foo",
			corev1.ServiceTypeClusterIP,
			"",
			"",
			map[string]string{"component": "foo"},
			map[string]string{
				HostnameAnnotationKey: "service.example.org",
			},
			corev1.ClusterIPNone,
			"1.1.1.1",
			map[string]string{
				"component": "foo",
			},
			[]string{},
			[]string{"foo-0", "foo-1"},
			[]string{"", ""},
			[]corev1.PodPhase{corev1.PodRunning, corev1.PodRunning},
			[]*endpoint.Endpoint{
				{DNSName: "service.example.org", Targets: endpoint.Targets{"1.1.1.1"}},
				{DNSName: "service.example.org", Targets: endpoint.Targets{"1.1.1.1"}},
			},
			false,
		},
	} {
		t.Run(tc.title, func(t *testing.T) {
			// Create a Kubernetes testing client
			kubernetes := fake.NewSimpleClientset()

			service := &corev1.Service{
				Spec: corev1.ServiceSpec{
					Type:      tc.svcType,
					ClusterIP: tc.clusterIP,
					Selector:  tc.selector,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   tc.svcNamespace,
					Name:        tc.svcName,
					Labels:      tc.labels,
					Annotations: tc.annotations,
				},
				Status: corev1.ServiceStatus{},
			}
			_, err := kubernetes.CoreV1().Services(service.Namespace).Create(service)
			require.NoError(t, err)

			for i, podname := range tc.podnames {
				pod := &corev1.Pod{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{},
						Hostname:   tc.hostnames[i],
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace:   tc.svcNamespace,
						Name:        podname,
						Labels:      tc.labels,
						Annotations: tc.annotations,
					},
					Status: corev1.PodStatus{
						PodIP: tc.podIP,
						Phase: tc.phases[i],
					},
				}

				_, err = kubernetes.CoreV1().Pods(tc.svcNamespace).Create(pod)
				require.NoError(t, err)
			}

			// Create our object under test and get the endpoints.
			client, _ := NewSource(
				kubernetes.CoreV1(),
				tc.targetNamespace,
				"",
				tc.fqdnTemplate,
				false,
				tc.compatibility,
				true,
			)
			require.NoError(t, err)

			endpoints, err := client.Endpoints()
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			// Validate returned endpoints against desired endpoints.
			ValidateEndpoints(t, endpoints, tc.expected)
		})
	}
}

func BenchmarkServiceEndpoints(b *testing.B) {
	kubernetes := fake.NewSimpleClientset()

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "testing",
			Name:      "foo",
			Annotations: map[string]string{
				HostnameAnnotationKey: "foo.example.org.",
			},
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{IP: "1.2.3.4"},
					{IP: "8.8.8.8"},
				},
			},
		},
	}

	_, err := kubernetes.CoreV1().Services(service.Namespace).Create(service)
	require.NoError(b, err)

	client, err := NewSource(kubernetes.CoreV1(), corev1.NamespaceAll, "", "", false, "", false)
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		_, err := client.Endpoints()
		require.NoError(b, err)
	}
}
