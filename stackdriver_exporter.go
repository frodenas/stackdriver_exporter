// Copyright 2020 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/PuerkitoBio/rehttp"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/monitoring/v3"
	"google.golang.org/api/option"
	"gopkg.in/alecthomas/kingpin.v2"

	"github.com/prometheus-community/stackdriver_exporter/collectors"
)

var (
	listenAddress = kingpin.Flag(
		"web.listen-address", "Address to listen on for web interface and telemetry ($STACKDRIVER_EXPORTER_WEB_LISTEN_ADDRESS).",
	).Envar("STACKDRIVER_EXPORTER_WEB_LISTEN_ADDRESS").Default(":9255").String()

	metricsPath = kingpin.Flag(
		"web.telemetry-path", "Path under which to expose Prometheus metrics ($STACKDRIVER_EXPORTER_WEB_TELEMETRY_PATH).",
	).Envar("STACKDRIVER_EXPORTER_WEB_TELEMETRY_PATH").Default("/metrics").String()

	projectID = kingpin.Flag(
		"google.project-id", "Comma seperated list of Google Project IDs ($STACKDRIVER_EXPORTER_GOOGLE_PROJECT_ID).",
	).Envar("STACKDRIVER_EXPORTER_GOOGLE_PROJECT_ID").String()

	stackdriverMaxRetries = kingpin.Flag(
		"stackdriver.max-retries", "Max number of retries that should be attempted on 503 errors from stackdriver. ($STACKDRIVER_EXPORTER_MAX_RETRIES)",
	).Envar("STACKDRIVER_EXPORTER_MAX_RETRIES").Default("0").Int()

	stackdriverHttpTimeout = kingpin.Flag(
		"stackdriver.http-timeout", "How long should stackdriver_exporter wait for a result from the Stackdriver API ($STACKDRIVER_EXPORTER_HTTP_TIMEOUT)",
	).Envar("STACKDRIVER_EXPORTER_HTTP_TIMEOUT").Default("10s").Duration()

	stackdriverMaxBackoffDuration = kingpin.Flag(
		"stackdriver.max-backoff", "Max time between each request in an exp backoff scenario ($STACKDRIVER_EXPORTER_MAX_BACKOFF_DURATION)",
	).Envar("STACKDRIVER_EXPORTER_MAX_BACKOFF_DURATION").Default("5s").Duration()

	stackdriverBackoffJitterBase = kingpin.Flag(
		"stackdriver.backoff-jitter", "The amount of jitter to introduce in a exp backoff scenario ($STACKDRIVER_EXPORTER_BACKODFF_JITTER_BASE)",
	).Envar("STACKDRIVER_EXPORTER_BACKODFF_JITTER_BASE").Default("1s").Duration()

	stackdriverRetryStatuses = kingpin.Flag(
		"stackdriver.retry-statuses", "The HTTP statuses that should trigger a retry ($STACKDRIVER_EXPORTER_RETRY_STATUSES)",
	).Envar("STACKDRIVER_EXPORTER_RETRY_STATUSES").Default("503").Ints()
)

func init() {
	prometheus.MustRegister(version.NewCollector("stackdriver_exporter"))
}

func getDefaultGCPProject(ctx context.Context) (*string, error) {
	credentials, err := google.FindDefaultCredentials(ctx, compute.ComputeScope)
	if err != nil {
		return nil, err
	}
	if credentials.ProjectID == "" {
		return nil, fmt.Errorf("unable to identify the gcloud project. Got empty string")
	}
	return &credentials.ProjectID, nil
}

func createMonitoringService(ctx context.Context) (*monitoring.Service, error) {
	googleClient, err := google.DefaultClient(ctx, monitoring.MonitoringReadScope)
	if err != nil {
		return nil, fmt.Errorf("Error creating Google client: %v", err)
	}

	googleClient.Timeout = *stackdriverHttpTimeout
	googleClient.Transport = rehttp.NewTransport(
		googleClient.Transport, // need to wrap DefaultClient transport
		rehttp.RetryAll(
			rehttp.RetryMaxRetries(*stackdriverMaxRetries),
			rehttp.RetryStatuses(*stackdriverRetryStatuses...)), // Cloud support suggests retrying on 503 errors
		rehttp.ExpJitterDelay(*stackdriverBackoffJitterBase, *stackdriverMaxBackoffDuration), // Set timeout to <10s as that is prom default timeout
	)

	monitoringService, err := monitoring.NewService(ctx, option.WithHTTPClient(googleClient))
	if err != nil {
		return nil, fmt.Errorf("Error creating Google Stackdriver Monitoring service: %v", err)
	}

	return monitoringService, nil
}

type handler struct {
	handler http.Handler
	logger  log.Logger

	projectIDs []string
	m          *monitoring.Service
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	collectParams := r.URL.Query()["collect"]
	filters := make(map[string]bool)
	for _, param := range collectParams {
		filters[param] = true
	}

	if len(filters) > 0 {
		h.innerHandler(filters).ServeHTTP(w, r)
		return
	}

	h.handler.ServeHTTP(w, r)
}

func newHandler(projectIDs []string, m *monitoring.Service, logger log.Logger) *handler {
	h := &handler{
		logger:     logger,
		projectIDs: projectIDs,
		m:          m,
	}

	h.handler = h.innerHandler(nil)
	return h
}

func (h *handler) innerHandler(filters map[string]bool) http.Handler {
	registry := prometheus.NewRegistry()

	for _, project := range h.projectIDs {
		monitoringCollector, err := collectors.NewMonitoringCollector(project, h.m, filters, h.logger)
		if err != nil {
			level.Error(h.logger).Log("err", err)
			os.Exit(1)
		}
		registry.MustRegister(monitoringCollector)
	}

	gatherers := prometheus.Gatherers{
		prometheus.DefaultGatherer,
		registry,
	}

	// Delegate http serving to Prometheus client library, which will call collector.Collect.
	return promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{})
}

func main() {
	promlogConfig := &promlog.Config{}
	flag.AddFlags(kingpin.CommandLine, promlogConfig)

	kingpin.Version(version.Print("stackdriver_exporter"))
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()

	logger := promlog.New(promlogConfig)

	ctx := context.Background()
	if *projectID == "" {
		level.Info(logger).Log("msg", "no projectID was provided. Trying to discover it")
		var err error
		projectID, err = getDefaultGCPProject(ctx)
		if err != nil {
			level.Error(logger).Log("msg", "no explicit projectID and error trying to discover default GCloud project", "err", err)
			os.Exit(1)
		}
	}

	level.Info(logger).Log("msg", "Starting stackdriver_exporter", "version", version.Info())
	level.Info(logger).Log("msg", "Build context", "build_context", version.BuildContext())
	level.Info(logger).Log("msg", "Using Google Cloud Project ID", "projectID", *projectID)

	monitoringService, err := createMonitoringService(ctx)
	if err != nil {
		level.Error(logger).Log("msg", "failed to create monitoring service", "err", err)
		os.Exit(1)
	}

	projectIDs := strings.Split(*projectID, ",")
	handler := newHandler(projectIDs, monitoringService, logger)

	http.Handle(*metricsPath, promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, handler))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Stackdriver Exporter</title></head>
             <body>
             <h1>Stackdriver Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})

	level.Info(logger).Log("msg", "Listening on", "address", *listenAddress)
	if err := http.ListenAndServe(*listenAddress, nil); err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}
}
