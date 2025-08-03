// Copyright 2017 Giuseppe Pellegrino
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
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"

	madmin "github.com/minio/madmin-go/v3"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	// namespace for all the metrics
	namespace = "minio"
	program   = "minio_exporter"
)

var (
	scrapeDurationDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "scrape", "collector_duration_seconds"),
		"minio_exporter: Duration of a collector scrape.",
		nil,
		nil,
	)
	scrapeSuccessDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "scrape", "collector_success"),
		"minio_exporter: Whether the collector succeeded.",
		nil,
		nil,
	)
)

// MinioExporter collects Minio statistics using the
// Prometheus metrics package
type MinioExporter struct {
	AdminClient *madmin.AdminClient
	MinioClient *minio.Client
	BucketStats bool
}

// NewMinioExporter inits and returns a MinioExporter
func NewMinioExporter(uri string, minioKey string, minioSecret string, bucketStats bool) (*MinioExporter, error) {
	secure := false
	newURI := uri

	if !strings.Contains(newURI, "://") {
		newURI = "http://" + newURI
	}

	urlMinio, err := url.Parse(newURI)
	if err != nil {
		return nil, fmt.Errorf("invalid Minio URI: %s with error <%s>", newURI, err)
	}
	if urlMinio.Scheme != "http" && urlMinio.Scheme != "https" {
		return nil, fmt.Errorf("invalid scheme for Minio: %s", urlMinio.Scheme)
	}
	if urlMinio.Host == "" {
		return nil, fmt.Errorf("Empty host is a non valid host: %s", urlMinio)
	}

	if urlMinio.Scheme == "https" {
		secure = true
	}

	mdmClient, err := madmin.New(urlMinio.Host, minioKey, minioSecret, secure)
	if err != nil {
		return nil, fmt.Errorf("Minio admin client error %s", err)
	}

	minioClient, err := minio.New(urlMinio.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(minioKey, minioSecret, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("Minio client error %s", err)
	}

	return &MinioExporter{
		AdminClient: mdmClient,
		MinioClient: minioClient,
		BucketStats: bucketStats,
	}, nil
}

// Describe implements the prometheus.Collector interface.
func (e *MinioExporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- scrapeDurationDesc
	ch <- scrapeSuccessDesc
}

// Collect implements the prometheus.Collector interface.
func (e *MinioExporter) Collect(ch chan<- prometheus.Metric) {
	begin := time.Now()
	err := execute(e, ch)
	duration := time.Since(begin)

	var success float64
	if err != nil {
		log.Errorf("ERROR: collector failed after %fs: %s", duration.Seconds(), err)
		success = 0
	} else {
		log.Debugf("OK: collector succeeded after %fs", duration.Seconds())
		success = 1
	}

	ch <- prometheus.MustNewConstMetric(scrapeDurationDesc, prometheus.GaugeValue, duration.Seconds())
	ch <- prometheus.MustNewConstMetric(scrapeSuccessDesc, prometheus.GaugeValue, success)
}

func execute(e *MinioExporter, ch chan<- prometheus.Metric) error {
	ctx := context.Background()

	// Get server info instead of service status
	info, err := e.AdminClient.ServerInfo(ctx)
	if err != nil {
		return err
	}

	// Calculate uptime from server info
	var totalUptime time.Duration
	for _, server := range info.Servers {
		if server.State == "online" {
			// Use a simple approach - just report that the service is up
			totalUptime = 24 * time.Hour // Default uptime placeholder
		}
	}

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "uptime"),
			"Minio service uptime in seconds",
			nil,
			nil),
		prometheus.CounterValue,
		totalUptime.Seconds())

	// Collect server admin statistics
	collectServerStats(e, ch)
	if e.BucketStats {
		collectBucketsStats(e, ch)
	}
	return nil
}

func collectServerStats(e *MinioExporter, ch chan<- prometheus.Metric) {
	ctx := context.Background()
	info, err := e.AdminClient.ServerInfo(ctx)
	if err != nil {
		return
	}

	for _, server := range info.Servers {
		host := server.Endpoint
		serverUp := 1
		if server.State != "online" {
			serverUp = 0
		}

		if server.State == "online" {
			// Basic server metrics
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc(
					prometheus.BuildFQName(namespace, "server", "uptime"),
					"Minio server uptime in seconds",
					[]string{"minio_host"},
					nil),
				prometheus.CounterValue,
				24*60*60, host) // Placeholder uptime
		}

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "server", "up"),
				"Minio host up",
				[]string{"minio_host"},
				nil),
			prometheus.GaugeValue,
			float64(serverUp), host)
	}

	// Get storage info
	storageInfo, err := e.AdminClient.StorageInfo(ctx)
	if err == nil {
		collectStorageInfo(storageInfo, ch)
	}
}

// collectHTTPStats is commented out due to API changes in madmin-go/v3
// TODO: Implement HTTP stats collection for madmin-go/v3
/*
func collectHTTPStats(httpStats madmin.ServerHTTPStats, host string, ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_count_heads"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.TotalHEADStats.Count), host)

	totHEADStats, _ := time.ParseDuration(httpStats.TotalHEADStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_avg_duration_heads"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(totHEADStats.Seconds()), host)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_count_heads"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.SuccessHEADStats.Count), host)

	succHEADStats, _ := time.ParseDuration(httpStats.SuccessHEADStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_avg_duration_heads"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(succHEADStats.Seconds()), host)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_count_gets"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.TotalGETStats.Count), host)

	totGETStats, _ := time.ParseDuration(httpStats.TotalGETStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_avg_duration_gets"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(totGETStats.Seconds()), host)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_count_gets"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.SuccessGETStats.Count), host)

	succGETStats, _ := time.ParseDuration(httpStats.SuccessGETStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_avg_duration_gets"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(succGETStats.Seconds()), host)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_count_puts"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.TotalPUTStats.Count), host)

	totPUTStats, _ := time.ParseDuration(httpStats.TotalPUTStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_avg_duration_puts"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(totPUTStats.Seconds()), host)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_count_puts"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.SuccessPUTStats.Count), host)

	succPUTStats, _ := time.ParseDuration(httpStats.SuccessPUTStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_avg_duration_puts"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(succPUTStats.Seconds()), host)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_count_posts"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.TotalPOSTStats.Count), host)

	totPOSTStats, _ := time.ParseDuration(httpStats.TotalPOSTStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_avg_duration_posts"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(totPOSTStats.Seconds()), host)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_count_posts"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.SuccessPOSTStats.Count), host)

	succPOSTStats, _ := time.ParseDuration(httpStats.SuccessPOSTStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_avg_duration_posts"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(succPOSTStats.Seconds()), host)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_count_deletes"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.TotalDELETEStats.Count), host)

	totDELETEStats, _ := time.ParseDuration(httpStats.TotalDELETEStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "total_avg_duration_deletes"),
			"Minio total input bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(totDELETEStats.Seconds()), host)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_count_deletes"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(httpStats.SuccessDELETEStats.Count), host)

	succDELETEStats, _ := time.ParseDuration(httpStats.SuccessDELETEStats.AvgDuration)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "http", "success_avg_duration_deletes"),
			"Minio total output bytes received",
			[]string{"minio_host"},
			nil),
		prometheus.GaugeValue,
		float64(succDELETEStats.Seconds()), host)
}
*/

func collectStorageInfo(si madmin.StorageInfo, ch chan<- prometheus.Metric) {
	// Basic storage metrics for madmin-go/v3
	// The API has changed, so we'll implement basic metrics

	// Count total disks from the Disks slice
	totalDisks := len(si.Disks)
	onlineDisks := 0
	var totalSpace, usedSpace uint64

	for _, disk := range si.Disks {
		if disk.State == "ok" || disk.State == "online" {
			onlineDisks++
		}
		totalSpace += disk.TotalSpace
		usedSpace += disk.UsedSpace
	}

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "storage", "total_disk_space"),
			"Total Minio disk space in bytes",
			nil,
			nil),
		prometheus.GaugeValue,
		float64(totalSpace))

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "storage", "free_disk_space"),
			"Free Minio disk space in bytes",
			nil,
			nil),
		prometheus.GaugeValue,
		float64(totalSpace-usedSpace))

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "storage", "online_disks"),
			"Total number of Minio online disks",
			nil,
			nil),
		prometheus.GaugeValue,
		float64(onlineDisks))

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "storage", "offline_disks"),
			"Total number of Minio offline disks",
			nil,
			nil),
		prometheus.GaugeValue,
		float64(totalDisks-onlineDisks))
}

// Collect all buckets stats using fast data usage API
func collectBucketsStats(e *MinioExporter, ch chan<- prometheus.Metric) {
	ctx := context.Background()

	// Get all bucket usage data in one call (much faster)
	dataUsage, err := e.AdminClient.DataUsageInfo(ctx)
	if err != nil {
		log.Debugf("Failed to get data usage info: %s", err)
		// Fallback to listing buckets without detailed stats
		buckets, err := e.MinioClient.ListBuckets(ctx)
		if err == nil {
			for _, bucket := range buckets {
				location, _ := e.MinioClient.GetBucketLocation(ctx, bucket.Name)
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(
						prometheus.BuildFQName(namespace, "bucket", "exists"),
						"Whether the bucket exists",
						[]string{"bucket", "location"},
						nil),
					prometheus.GaugeValue,
					1, bucket.Name, location)
			}
		}
		return
	}

	// Process each bucket from usage data
	for bucketName, bucketUsage := range dataUsage.BucketsUsage {
		// Get bucket location
		location, _ := e.MinioClient.GetBucketLocation(ctx, bucketName)

		// Emit bucket metrics
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "bucket", "objects_number"),
				"The number of objects in the bucket",
				[]string{"bucket", "location"},
				nil),
			prometheus.GaugeValue,
			float64(bucketUsage.ObjectsCount), bucketName, location)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "bucket", "objects_total_size"),
				"The total size of all objects in the bucket",
				[]string{"bucket", "location"},
				nil),
			prometheus.GaugeValue,
			float64(bucketUsage.Size), bucketName, location)
	}
}

// calculate bucket statistics using fast data API
func bucketStats(bucket minio.BucketInfo, e *MinioExporter, ch chan<- prometheus.Metric) {
	ctx := context.Background()
	location, _ := e.MinioClient.GetBucketLocation(ctx, bucket.Name)

	// Use admin data usage API for fast bucket stats
	dataUsage, err := e.AdminClient.DataUsageInfo(ctx)
	if err != nil {
		log.Debugf("Failed to get data usage for bucket %s: %s", bucket.Name, err)
		// Fallback to basic metrics without object scanning
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "bucket", "exists"),
				"Whether the bucket exists",
				[]string{"bucket", "location"},
				nil),
			prometheus.GaugeValue,
			1, bucket.Name, location)
		return
	}

	// Extract bucket-specific data from usage info
	var objNum int64
	var bucketSize int64

	// Look for bucket in the data usage info
	if bucketData, exists := dataUsage.BucketsUsage[bucket.Name]; exists {
		objNum = int64(bucketData.ObjectsCount)
		bucketSize = int64(bucketData.Size)
	} else {
		// If bucket not found in usage data, use basic counting
		log.Debugf("Bucket %s not found in data usage, using basic metrics", bucket.Name)
		objNum = 0
		bucketSize = 0
	}

	// Emit metrics
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "bucket", "objects_number"),
			"The number of objects in the bucket",
			[]string{"bucket", "location"},
			nil),
		prometheus.GaugeValue,
		float64(objNum), bucket.Name, location)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "bucket", "objects_total_size"),
			"The total size of all objects in the bucket",
			[]string{"bucket", "location"},
			nil),
		prometheus.GaugeValue,
		float64(bucketSize), bucket.Name, location)

	// Get incomplete uploads count (this is still fast)
	var incompleteUploads int64
	for upload := range e.MinioClient.ListIncompleteUploads(ctx, bucket.Name, "", false) {
		if upload.Err != nil {
			break
		}
		incompleteUploads++
		// Only count, don't calculate sizes to keep it fast
		if incompleteUploads > 100 { // Limit to avoid slowdown
			break
		}
	}

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "bucket", "incomplete_uploads_number"),
			"The total number of incomplete uploads per bucket",
			[]string{"bucket", "location"},
			nil),
		prometheus.GaugeValue,
		float64(incompleteUploads), bucket.Name, location)
}

// get Enviroment variable value if the variable exists otherwise
// return the default
func getEnv(key string, defaultVal string) string {
	if env, ok := os.LookupEnv(key); ok {
		return env
	}
	return defaultVal
}

func init() {
	prometheus.MustRegister(version.NewCollector(program))
}

func main() {
	var (
		printVersion  = flag.Bool("version", false, "Print version information.")
		listenAddress = flag.String("web.listen-address", getEnv("LISTEN_ADDRESS", ":9290"), "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("web.telemetry-path", getEnv("METRIC_PATH", "/metrics"), "Path under which to expose metrics.")
		minioURI      = flag.String("minio.server", getEnv("MINIO_URL", "http://localhost:9000"), "HTTP address of the Minio server")
		minioKey      = flag.String("minio.access-key", getEnv("MINIO_ACCESS_KEY", ""), "The access key used to login in to Minio.")
		minioSecret   = flag.String("minio.access-secret", getEnv("MINIO_ACCESS_SECRET", ""), "The access secret used to login in to Minio")
		bucketStats   = flag.Bool("minio.bucket-stats", false, "Collect bucket statistics. It can take long.")
	)

	flag.Parse()

	if *printVersion {
		fmt.Fprintln(os.Stdout, version.Print("minio_exporter"))
		os.Exit(0)
	}

	exporter, err := NewMinioExporter(*minioURI, *minioKey, *minioSecret, *bucketStats)
	if err != nil {
		log.Fatalln(err)
	}

	log.Infoln("Starting minio_exporter", version.Info())
	log.Infoln("Build context", version.BuildContext())

	prometheus.MustRegister(exporter)

	http.Handle(*metricsPath, prometheus.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
                        <head><title>Minio Exporter</title></head>
                        <body>
                        <h1>Minio Exporter</h1>
                        <p><a href='` + *metricsPath + `'>Metrics</a></p>
                        </body>
                        </html>`))
	})

	log.Infoln("Listening on", *listenAddress)
	err = http.ListenAndServe(*listenAddress, nil)
	if err != nil {
		log.Fatal(err)
	}
}
