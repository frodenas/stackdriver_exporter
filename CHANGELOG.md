## master / unreleased
* [FEATURE] Add support to include ingest delay when pull metrics #129

## 0.11.0 / 2020-09-02

* [CHANGE] Do not treat failure to collect metrics as fatal #102
* [FEATURE] Add support for multiple google project IDs #105

## 0.10.0 / 2020-06-28

* [FEATURE] Autodiscover Google Poject ID #62

## 0.9.1 / 2020-06-02

* [BUGFIX] Fix report time missing for histogram metrics #94

## 0.9.0 / 2020-05-26

* [CHANGE] Add stackdriver timestamp to metrics #84
* [CHANGE] Fix collect param name #91

## 0.8.0 / 2020-05-13

* [CHANGE] Treat failure to collect metric as fatal #83
* [CHANGE] Switch logging to promlog #88
* [FEATURE] Add metrics prefix collect URL param #87

## 0.7.0 / 2020-05-01

* [CHANGE] Remove deprecated `monitoring.New()` use. #76
* [ENHANCEMENT] Server-side selection of project's metrics #53
* [BUGFIX] Ensure metrics are fetched once for each metric descriptor #50
  
## 0.6.0 / 2018-12-02

Google Stackdriver Prometheus Exporter v0.6.0:

* Added a `collector.fill-missing-labels` flag to fill missing metrics labels with empty strings in order to avoid label dimensions inconsistent failure (PR https://github.com/frodenas/stackdriver_exporter/pull/23)
* Added `stackdriver.max-retries`, `stackdriver.http-timeout`, `stackdriver.max-backoff`, `stackdriver.backoff-jitter`, and`stackdriver.retry-statuses` flags to allow exponential backoff and retries on stackdriver api (PR https://github.com/frodenas/stackdriver_exporter/pull/35)
* Added a `monitoring.drop-delegated-projects` flag which allows one to disable metrics collection from delegated projects (PR https://github.com/frodenas/stackdriver_exporter/pull/40)
* Fix segmentation fault on missing credentials (PR https://github.com/frodenas/stackdriver_exporter/pull/42)
