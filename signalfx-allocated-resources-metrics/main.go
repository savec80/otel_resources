package main

import (
	"context"
	"flag"
	"fmt"
	_ "net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/signalfx/golib/datapoint"
	"github.com/signalfx/golib/sfxclient"

	"github.com/golang/glog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	kubeconfig         string
	collectorNamespace string
	signalfxCfgSecret  string
)

type AllocatedResourceCollector struct {
	kubeClient               *kubernetes.Clientset
	allocatedResourcesClient *AllocatedResourcesClient
}

func createConfigAndClientOrDie(kubeconfig string) *kubernetes.Clientset {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		glog.Fatalf("Failed to create config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Fatalf("Failed to create client: %v", err)
	}
	return clientset
}

func handleFlags() {
	flag.StringVar(&kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&collectorNamespace, "namespace", getEnv("NAMESPACE", "paas-monitoring"), "The SignalFX collector namespace")
	flag.StringVar(&signalfxCfgSecret, "signalfxCfgSecret", getEnv("SIGNALFX_CONFIG_SECRET", "signalfx-allocated-resource-metrics-collector"), "The reference to the secret containing the SignalFX monitoring configuration.")

	if !flag.Parsed() {
		flag.Parse()
	}
}

func main() {
	defer glog.Flush()

	handleFlags()

	glog.Infof("MP+ Allocated Resources Metrics Collector - started")

	kubeClient := createConfigAndClientOrDie(kubeconfig)
	options := &Options{
		GetTotalsByInstanceType: true,
		GetNodeDetails:          false,
	}

	allocatedResourcesClient := NewAllocatedResourcesClient(kubeClient, options)

	collector := AllocatedResourceCollector{kubeClient, allocatedResourcesClient}

	stopCh := handleSignals()

	// get signalfx config secret
	signalfxCfg, err := collector.kubeClient.CoreV1().Secrets(collectorNamespace).Get(context.TODO(), signalfxCfgSecret, metav1.GetOptions{})
	if err == nil {
		glog.Infof("Cluster ID: %s", string(signalfxCfg.Data["cluster_name"]))
		collector.initializeMonitoring(signalfxCfg)
	} else {
		glog.Infof("MP+ Allocated Resources Metrics Collector is not active: %s ", err.Error())
	}

	<-stopCh
	glog.Infof("MP+ Allocated Resources Metrics Collector - exited")
}

// Shutdown gracefully on system signals.
func handleSignals() <-chan struct{} {
	sigCh := make(chan os.Signal, 1)
	stopCh := make(chan struct{})
	go func() {
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGTERM)
		<-sigCh
		close(stopCh)
		os.Exit(1)
	}()
	return stopCh
}

func (c *AllocatedResourceCollector) Datapoints() []*datapoint.Datapoint {
	allocatedResourcesMetrics, err := c.getAllocatedResources()
	if err != nil {
		return []*datapoint.Datapoint{}
	}

	dataPoints := []*datapoint.Datapoint{}
	for _, group := range allocatedResourcesMetrics.Groups {
		instanceSize := group.InstanceType
		parts := strings.Split(instanceSize, ".")
		if len(parts) > 1 {
			instanceSize = parts[1]
		}

		dimensions := map[string]string{
			"node_kubernetes_io_instance-type": group.InstanceType,
			"node_kubernetes_io_instance-size": instanceSize,
			"node_kubernetes_io_node-role":     group.NodeRole,
			// If empty because the label on the node does not exist, this means
			// the node is not assigned to a specific tenant.
			"tenant_paas_redhat_com_tenant": group.Labels["tenant.paas.redhat.com/tenant"],
		}

		dataPoints = append(dataPoints, sfxclient.Gauge("paas.cluster_node_count", dimensions, int64(group.NodeCount)))
		dataPoints = append(dataPoints, sfxclient.Gauge("paas.cluster_memory_total", dimensions, group.MemoryTotal))
		dataPoints = append(dataPoints, sfxclient.Gauge("paas.cluster_memory_requests", dimensions, group.MemoryRequests))
		dataPoints = append(dataPoints, sfxclient.Gauge("paas.cluster_memory_limits", dimensions, group.MemoryLimits))
		dataPoints = append(dataPoints, sfxclient.Gauge("paas.cluster_cpu_total", dimensions, group.CPUTotal))
		dataPoints = append(dataPoints, sfxclient.Gauge("paas.cluster_cpu_requests", dimensions, group.CPURequests))
		dataPoints = append(dataPoints, sfxclient.Gauge("paas.cluster_cpu_limits", dimensions, group.CPULimits))
	}
	return dataPoints
}

func (c AllocatedResourceCollector) initializeMonitoring(signalfxCfg *corev1.Secret) {
	scheduler := sfxclient.NewScheduler()
	scheduler.DefaultDimensions(map[string]string{
		"kubernetes_cluster": string(signalfxCfg.Data["cluster_name"]),
		"cluster":            string(signalfxCfg.Data["cluster_name"]),
	})
	scheduler.AddCallback(&c)

	// Configure sink
	sink, _ := scheduler.Sink.(*sfxclient.HTTPSink)
	sink.DatapointEndpoint = fmt.Sprintf("https://ingest.%s.signalFx.com/v2/datapoint", string(signalfxCfg.Data["signalfx_realm"]))
	sink.AuthToken = string(signalfxCfg.Data["signalfx_auth_token"])

	reportingDelay, err := strconv.Atoi(string(signalfxCfg.Data["reporting_delay_seconds"]))
	if err != nil {
		// default to 5 minutes
		reportingDelay = 300
	}

	reportingTimeout, err := strconv.Atoi(string(signalfxCfg.Data["reporting_timeout_seconds"]))
	if err != nil {
		// default to 1 second
		reportingTimeout = 1
	}

	glog.Infof("Reporting Delay %d seconds", reportingDelay)
	glog.Infof("Reporting Timeout: %d seconds", reportingTimeout)

	scheduler.ReportingDelay(time.Duration(reportingDelay) * time.Second)
	scheduler.ReportingTimeout(time.Duration(reportingTimeout) * time.Second)
	go func() {
		err := scheduler.Schedule(context.Background())
		if err != nil {
			glog.Errorf("Scheduler returned an error: %s", err.Error())
		}
	}()
}

func (c AllocatedResourceCollector) getAllocatedResources() (ClusterMetrics, error) {
	return c.allocatedResourcesClient.GetAllocatedResources()
}

// getEnv get key environment variable if exist otherwise return defalutValue
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		return defaultValue
	}
	return value
}
