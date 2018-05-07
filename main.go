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

package main

import (
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/kubernetes-incubator/external-dns/controller"
	"github.com/kubernetes-incubator/external-dns/pkg/apis/externaldns"
	"github.com/kubernetes-incubator/external-dns/pkg/apis/externaldns/validation"
	"github.com/kubernetes-incubator/external-dns/plan"
	"github.com/kubernetes-incubator/external-dns/provider"
	"github.com/kubernetes-incubator/external-dns/registry"
	"github.com/kubernetes-incubator/external-dns/source"
	"github.com/kubernetes-incubator/external-dns/source/clientgenerator"

	"github.com/kubernetes-incubator/external-dns/provider/aws"
	"github.com/kubernetes-incubator/external-dns/provider/azure"
	"github.com/kubernetes-incubator/external-dns/provider/cloudflare"
	"github.com/kubernetes-incubator/external-dns/provider/designate"
	"github.com/kubernetes-incubator/external-dns/provider/digitalocean"
	"github.com/kubernetes-incubator/external-dns/provider/dnsimple"
	"github.com/kubernetes-incubator/external-dns/provider/dyn"
	"github.com/kubernetes-incubator/external-dns/provider/google"
	"github.com/kubernetes-incubator/external-dns/provider/infoblox"
	"github.com/kubernetes-incubator/external-dns/provider/inmemory"
	"github.com/kubernetes-incubator/external-dns/provider/pdns"

	"github.com/kubernetes-incubator/external-dns/source/fake"
	"github.com/kubernetes-incubator/external-dns/source/ingress"
	"github.com/kubernetes-incubator/external-dns/source/service"
)

// ErrSourceNotFound is returned when a requested source doesn't exist.
var ErrSourceNotFound = errors.New("source not found")

func main() {
	cfg := externaldns.NewConfig()
	if err := cfg.ParseFlags(os.Args[1:]); err != nil {
		log.Fatalf("flag parsing error: %v", err)
	}
	log.Infof("config: %s", cfg)

	if err := validation.ValidateConfig(cfg); err != nil {
		log.Fatalf("config validation failed: %v", err)
	}

	if cfg.LogFormat == "json" {
		log.SetFormatter(&log.JSONFormatter{})
	}
	if cfg.DryRun {
		log.Info("running in dry-run mode. No changes to DNS records will be made.")
	}

	ll, err := log.ParseLevel(cfg.LogLevel)
	if err != nil {
		log.Fatalf("failed to parse log level: %v", err)
	}
	log.SetLevel(ll)

	stopChan := make(chan struct{}, 1)

	go serveMetrics(cfg.MetricsAddress)
	go handleSigterm(stopChan)

	// Create a source.Config from the flags passed by the user.
	sourceCfg := &source.Config{
		Namespace:                cfg.Namespace,
		AnnotationFilter:         cfg.AnnotationFilter,
		FQDNTemplate:             cfg.FQDNTemplate,
		CombineFQDNAndAnnotation: cfg.CombineFQDNAndAnnotation,
		Compatibility:            cfg.Compatibility,
		PublishInternal:          cfg.PublishInternal,
	}

	// Lookup all the selected sources by names and pass them the desired configuration.
	sources, err := ByNames(&clientgenerator.SingletonClientGenerator{
		KubeConfig: cfg.KubeConfig,
		KubeMaster: cfg.Master,
	}, cfg.Sources, sourceCfg)
	if err != nil {
		log.Fatal(err)
	}

	// Combine multiple sources into a single, deduplicated source.
	endpointsSource := source.NewDedupSource(source.NewMultiSource(sources))

	domainFilter := provider.NewDomainFilter(cfg.DomainFilter)
	zoneIDFilter := provider.NewZoneIDFilter(cfg.ZoneIDFilter)
	zoneTypeFilter := provider.NewZoneTypeFilter(cfg.AWSZoneType)

	var p provider.Provider
	switch cfg.Provider {
	case "aws":
		p, err = aws.NewProvider(domainFilter, zoneIDFilter, zoneTypeFilter, cfg.AWSAssumeRole, cfg.DryRun)
	case "azure":
		p, err = azure.NewProvider(cfg.AzureConfigFile, domainFilter, zoneIDFilter, cfg.AzureResourceGroup, cfg.DryRun)
	case "cloudflare":
		p, err = cloudflare.NewProvider(domainFilter, zoneIDFilter, cfg.CloudflareProxied, cfg.DryRun)
	case "google":
		p, err = google.NewProvider(cfg.GoogleProject, domainFilter, zoneIDFilter, cfg.DryRun)
	case "digitalocean":
		p, err = digitalocean.NewProvider(domainFilter, cfg.DryRun)
	case "dnsimple":
		p, err = dnsimple.NewProvider(domainFilter, zoneIDFilter, cfg.DryRun)
	case "infoblox":
		p, err = infoblox.NewProvider(
			infoblox.Config{
				DomainFilter: domainFilter,
				ZoneIDFilter: zoneIDFilter,
				Host:         cfg.InfobloxGridHost,
				Port:         cfg.InfobloxWapiPort,
				Username:     cfg.InfobloxWapiUsername,
				Password:     cfg.InfobloxWapiPassword,
				Version:      cfg.InfobloxWapiVersion,
				SSLVerify:    cfg.InfobloxSSLVerify,
				DryRun:       cfg.DryRun,
			},
		)
	case "dyn":
		p, err = dyn.NewProvider(
			dyn.Config{
				DomainFilter:  domainFilter,
				ZoneIDFilter:  zoneIDFilter,
				DryRun:        cfg.DryRun,
				CustomerName:  cfg.DynCustomerName,
				Username:      cfg.DynUsername,
				Password:      cfg.DynPassword,
				MinTTLSeconds: cfg.DynMinTTLSeconds,
				AppVersion:    externaldns.Version,
			},
		)
	case "inmemory":
		p, err = inmemory.NewProvider(inmemory.InitZones(cfg.InMemoryZones), inmemory.WithDomain(domainFilter), inmemory.WithLogging()), nil
	case "designate":
		p, err = designate.NewProvider(domainFilter, cfg.DryRun)
	case "pdns":
		p, err = pdns.NewProvider(cfg.PDNSServer, cfg.PDNSAPIKey, domainFilter, cfg.DryRun)
	default:
		log.Fatalf("unknown dns provider: %s", cfg.Provider)
	}
	if err != nil {
		log.Fatal(err)
	}

	var r registry.Registry
	switch cfg.Registry {
	case "noop":
		r, err = registry.NewNoopRegistry(p)
	case "txt":
		r, err = registry.NewTXTRegistry(p, cfg.TXTPrefix, cfg.TXTOwnerID)
	default:
		log.Fatalf("unknown registry: %s", cfg.Registry)
	}

	if err != nil {
		log.Fatal(err)
	}

	policy, exists := plan.Policies[cfg.Policy]
	if !exists {
		log.Fatalf("unknown policy: %s", cfg.Policy)
	}

	ctrl := controller.Controller{
		Source:   endpointsSource,
		Registry: r,
		Policy:   policy,
		Interval: cfg.Interval,
	}

	if cfg.Once {
		err := ctrl.RunOnce()
		if err != nil {
			log.Fatal(err)
		}

		os.Exit(0)
	}

	ctrl.Run(stopChan)
}

// ByNames returns multiple Sources given multiple names.
func ByNames(p clientgenerator.ClientGenerator, names []string, cfg *source.Config) ([]source.Source, error) {
	sources := []source.Source{}
	for _, name := range names {
		source, err := BuildWithConfig(name, p, cfg)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}

	return sources, nil
}

// BuildWithConfig allows to generate a Source implementation from the shared config
func BuildWithConfig(source string, p clientgenerator.ClientGenerator, cfg *source.Config) (s source.Source, err error) {
	switch source {
	case "service":
		client, err := p.KubeClient()
		if err != nil {
			return nil, err
		}
		s, err = service.NewSource(client, cfg.Namespace, cfg.AnnotationFilter, cfg.FQDNTemplate, cfg.CombineFQDNAndAnnotation, cfg.Compatibility, cfg.PublishInternal)
	case "ingress":
		client, err := p.KubeClient()
		if err != nil {
			return nil, err
		}
		s, err = ingress.NewSource(client, cfg.Namespace, cfg.AnnotationFilter, cfg.FQDNTemplate, cfg.CombineFQDNAndAnnotation)
	case "fake":
		s, err = fake.NewSource(cfg.FQDNTemplate)
	default:
		err = ErrSourceNotFound
	}
	return s, err
}

func handleSigterm(stopChan chan struct{}) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	<-signals
	log.Info("Received SIGTERM. Terminating...")
	close(stopChan)
}

func serveMetrics(address string) {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	http.Handle("/metrics", promhttp.Handler())

	log.Fatal(http.ListenAndServe(address, nil))
}
