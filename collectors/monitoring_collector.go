package collectors

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"golang.org/x/net/context"
	"google.golang.org/api/monitoring/v3"

	"github.com/frodenas/stackdriver_exporter/utils"
)

type MonitoringCollector struct {
	projectID                       string
	metricsTypePrefixes             []string
	metricsInterval                 time.Duration
	monitoringService               *monitoring.Service
	apiCallsTotalMetric             prometheus.Counter
	scrapesTotalMetric              prometheus.Counter
	scrapeErrorsTotalMetric         prometheus.Counter
	lastScrapeErrorMetric           prometheus.Gauge
	lastScrapeTimestampMetric       prometheus.Gauge
	lastScrapeDurationSecondsMetric prometheus.Gauge
}

func NewMonitoringCollector(projectID string, metricsTypePrefixes []string, metricsInterval time.Duration, monitoringService *monitoring.Service) (*MonitoringCollector, error) {
	apiCallsTotalMetric := prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace:   "stackdriver",
			Subsystem:   "monitoring",
			Name:        "api_calls_total",
			Help:        "Total number of Google Stackdriver Monitoring API calls made.",
			ConstLabels: prometheus.Labels{"project_id": projectID},
		},
	)

	scrapesTotalMetric := prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace:   "stackdriver",
			Subsystem:   "monitoring",
			Name:        "scrapes_total",
			Help:        "Total number of Google Stackdriver Monitoring metrics scrapes.",
			ConstLabels: prometheus.Labels{"project_id": projectID},
		},
	)

	scrapeErrorsTotalMetric := prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace:   "stackdriver",
			Subsystem:   "monitoring",
			Name:        "scrape_errors_total",
			Help:        "Total number of Google Stackdriver Monitoring metrics scrape errors.",
			ConstLabels: prometheus.Labels{"project_id": projectID},
		},
	)

	lastScrapeErrorMetric := prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace:   "stackdriver",
			Subsystem:   "monitoring",
			Name:        "last_scrape_error",
			Help:        "Whether the last metrics scrape from Google Stackdriver Monitoring resulted in an error (1 for error, 0 for success).",
			ConstLabels: prometheus.Labels{"project_id": projectID},
		},
	)

	lastScrapeTimestampMetric := prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace:   "stackdriver",
			Subsystem:   "monitoring",
			Name:        "last_scrape_timestamp",
			Help:        "Number of seconds since 1970 since last metrics scrape from Google Stackdriver Monitoring.",
			ConstLabels: prometheus.Labels{"project_id": projectID},
		},
	)

	lastScrapeDurationSecondsMetric := prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace:   "stackdriver",
			Subsystem:   "monitoring",
			Name:        "last_scrape_duration_seconds",
			Help:        "Duration of the last metrics scrape from Google Stackdriver Monitoring.",
			ConstLabels: prometheus.Labels{"project_id": projectID},
		},
	)

	monitoringCollector := &MonitoringCollector{
		projectID:                       projectID,
		metricsTypePrefixes:             metricsTypePrefixes,
		metricsInterval:                 metricsInterval,
		monitoringService:               monitoringService,
		apiCallsTotalMetric:             apiCallsTotalMetric,
		scrapesTotalMetric:              scrapesTotalMetric,
		scrapeErrorsTotalMetric:         scrapeErrorsTotalMetric,
		lastScrapeErrorMetric:           lastScrapeErrorMetric,
		lastScrapeTimestampMetric:       lastScrapeTimestampMetric,
		lastScrapeDurationSecondsMetric: lastScrapeDurationSecondsMetric,
	}

	return monitoringCollector, nil
}

func (c *MonitoringCollector) Describe(ch chan<- *prometheus.Desc) {
	c.apiCallsTotalMetric.Describe(ch)
	c.scrapesTotalMetric.Describe(ch)
	c.scrapeErrorsTotalMetric.Describe(ch)
	c.lastScrapeErrorMetric.Describe(ch)
	c.lastScrapeTimestampMetric.Describe(ch)
	c.lastScrapeDurationSecondsMetric.Describe(ch)
}

func (c *MonitoringCollector) Collect(ch chan<- prometheus.Metric) {
	var begun = time.Now()

	errorMetric := float64(0)
	if err := c.reportMonitoringMetrics(ch); err != nil {
		errorMetric = float64(1)
		c.scrapeErrorsTotalMetric.Inc()
		log.Errorf("Error while getting Google Stackdriver Monitoring metrics: %s", err)
	}
	c.scrapeErrorsTotalMetric.Collect(ch)

	c.apiCallsTotalMetric.Collect(ch)

	c.scrapesTotalMetric.Inc()
	c.scrapesTotalMetric.Collect(ch)

	c.lastScrapeErrorMetric.Set(errorMetric)
	c.lastScrapeErrorMetric.Collect(ch)

	c.lastScrapeTimestampMetric.Set(float64(time.Now().Unix()))
	c.lastScrapeTimestampMetric.Collect(ch)

	c.lastScrapeDurationSecondsMetric.Set(time.Since(begun).Seconds())
	c.lastScrapeDurationSecondsMetric.Collect(ch)
}

func (c *MonitoringCollector) reportMonitoringMetrics(ch chan<- prometheus.Metric) error {
	metricDescriptorsFunction := func(page *monitoring.ListMetricDescriptorsResponse) error {
		var wg = &sync.WaitGroup{}

		c.apiCallsTotalMetric.Inc()

		doneChannel := make(chan bool, 1)
		errChannel := make(chan error, 1)

		startTime := time.Now().UTC().Add(c.metricsInterval * -1)
		endTime := time.Now().UTC()

		for _, metricDescriptor := range page.MetricDescriptors {
			wg.Add(1)
			go func(metricDescriptor *monitoring.MetricDescriptor, ch chan<- prometheus.Metric) {
				defer wg.Done()
				log.Debugf("Retrieving Google Stackdriver Monitoring metrics for descriptor `%s`...", metricDescriptor.Type)
				timeSeriesListCall := c.monitoringService.Projects.TimeSeries.List(utils.ProjectResource(c.projectID)).
					Filter(fmt.Sprintf("metric.type=\"%s\"", metricDescriptor.Type)).
					IntervalStartTime(startTime.Format(time.RFC3339Nano)).
					IntervalEndTime(endTime.Format(time.RFC3339Nano))

				for {
					c.apiCallsTotalMetric.Inc()
					page, err := timeSeriesListCall.Do()
					if err != nil {
						errChannel <- err
						break
					}
					if page == nil {
						break
					}
					select {
					case <-errChannel:
						break
					default:
					}
					if err := c.reportTimeSeriesMetrics(page, metricDescriptor, ch); err != nil {
						errChannel <- err
						break
					}
					if page.NextPageToken == "" {
						break
					}
					timeSeriesListCall.PageToken(page.NextPageToken)
				}
			}(metricDescriptor, ch)
		}

		go func() {
			wg.Wait()
			close(doneChannel)
		}()

		select {
		case <-doneChannel:
		case err := <-errChannel:
			return err
		}

		return nil
	}

	var wg = &sync.WaitGroup{}

	doneChannel := make(chan bool, 1)
	errChannel := make(chan error, 1)

	for _, metricsTypePrefix := range c.metricsTypePrefixes {
		wg.Add(1)
		go func(metricsTypePrefix string) {
			defer wg.Done()
			log.Debugf("Listing Google Stackdriver Monitoring metric descriptors starting with `%s`...", metricsTypePrefix)
			ctx := context.Background()
			if err := c.monitoringService.Projects.MetricDescriptors.List(utils.ProjectResource(c.projectID)).
				Filter(fmt.Sprintf("metric.type = starts_with(\"%s\")", metricsTypePrefix)).
				Pages(ctx, metricDescriptorsFunction); err != nil {
				errChannel <- err
			}
		}(metricsTypePrefix)
	}

	go func() {
		wg.Wait()
		close(doneChannel)
	}()

	select {
	case <-doneChannel:
	case err := <-errChannel:
		return err
	}

	return nil
}

func (c *MonitoringCollector) reportTimeSeriesMetrics(page *monitoring.ListTimeSeriesResponse, metricDescriptor *monitoring.MetricDescriptor, ch chan<- prometheus.Metric) error {
	var metricValue float64
	var metricValueType prometheus.ValueType
	var newestTSPoint *monitoring.Point

	for _, timeSeries := range page.TimeSeries {
		newestEndTime := time.Unix(0, 0)
		for _, point := range timeSeries.Points {
			endTime, err := time.Parse(time.RFC3339Nano, point.Interval.EndTime)
			if err != nil {
				return errors.New(fmt.Sprintf("Error parsing TimeSeries Point interval end time `%s`: %s", point.Interval.EndTime, err))
			}
			if endTime.After(newestEndTime) {
				newestEndTime = endTime
				newestTSPoint = point
			}
		}

		switch timeSeries.MetricKind {
		case "GAUGE":
			metricValueType = prometheus.GaugeValue
		case "DELTA":
			metricValueType = prometheus.CounterValue
		case "CUMULATIVE":
			metricValueType = prometheus.CounterValue
		default:
			continue
		}

		switch timeSeries.ValueType {
		case "BOOL":
			metricValue = 0
			if *newestTSPoint.Value.BoolValue {
				metricValue = 1
			}
		case "INT64":
			metricValue = float64(*newestTSPoint.Value.Int64Value)
		case "DOUBLE":
			metricValue = *newestTSPoint.Value.DoubleValue
		default:
			log.Debugf("Discarding `%s` metric: %+v", timeSeries.ValueType, timeSeries)
			continue
		}

		labelKeys := []string{"unit", "resource_type"}
		labelValues := []string{metricDescriptor.Unit, timeSeries.Resource.Type}
		for key, value := range timeSeries.Metric.Labels {
			labelKeys = append(labelKeys, key)
			labelValues = append(labelValues, value)
		}
		for key, value := range timeSeries.Resource.Labels {
			labelKeys = append(labelKeys, key)
			labelValues = append(labelValues, value)
		}

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				prometheus.BuildFQName("stackdriver", "monitoring", utils.NormalizeMetricName(timeSeries.Metric.Type)),
				metricDescriptor.Description,
				labelKeys,
				prometheus.Labels{},
			),
			metricValueType,
			metricValue,
			labelValues...,
		)
	}

	return nil
}
