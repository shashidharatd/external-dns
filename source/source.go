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

package source

import (
	"fmt"
	"math"
	"net"
	"strconv"

	"github.com/kubernetes-incubator/external-dns/endpoint"
)

const (
	// The annotation used for figuring out which controller is responsible
	ControllerAnnotationKey = "external-dns.alpha.kubernetes.io/controller"
	// The annotation used for defining the desired hostname
	HostnameAnnotationKey = "external-dns.alpha.kubernetes.io/hostname"
	// The annotation used for defining the desired ingress target
	TargetAnnotationKey = "external-dns.alpha.kubernetes.io/target"
	// The annotation used for defining the desired DNS record TTL
	TTLAnnotationKey = "external-dns.alpha.kubernetes.io/ttl"
	// The value of the controller annotation so that we feel responsible
	ControllerAnnotationValue = "dns-controller"
)

const (
	ttlMinimum = 1
	ttlMaximum = math.MaxUint32
)

// Source defines the interface Endpoint sources should implement.
type Source interface {
	Endpoints() ([]*endpoint.Endpoint, error)
}

// Config holds shared configuration options for all Sources.
type Config struct {
	Namespace                string
	AnnotationFilter         string
	FQDNTemplate             string
	CombineFQDNAndAnnotation bool
	Compatibility            string
	PublishInternal          bool
}

func GetTTLFromAnnotations(annotations map[string]string) (endpoint.TTL, error) {
	ttlNotConfigured := endpoint.TTL(0)
	ttlAnnotation, exists := annotations[TTLAnnotationKey]
	if !exists {
		return ttlNotConfigured, nil
	}
	ttlValue, err := strconv.ParseInt(ttlAnnotation, 10, 64)
	if err != nil {
		return ttlNotConfigured, fmt.Errorf("\"%v\" is not a valid TTL value", ttlAnnotation)
	}
	if ttlValue < ttlMinimum || ttlValue > ttlMaximum {
		return ttlNotConfigured, fmt.Errorf("TTL value must be between [%d, %d]", ttlMinimum, ttlMaximum)
	}
	return endpoint.TTL(ttlValue), nil
}

// SuitableType returns the DNS resource record type suitable for the target.
// In this case type A for IPs and type CNAME for everything else.
func SuitableType(target string) string {
	if net.ParseIP(target) != nil {
		return endpoint.RecordTypeA
	}
	return endpoint.RecordTypeCNAME
}
