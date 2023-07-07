package main

import (
	"context"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
)

const instanceTypeLabel = "node.kubernetes.io/instance-type"
const nodeRoleMaster = "node-role.kubernetes.io/master"
const nodeRoleInfra = "node-role.kubernetes.io/infra"
const nodeRoleWorker = "node-role.kubernetes.io/worker"
const serviceComponentLabel = "servicecomponent"

type Options struct {
	Labels                  string
	IgnoreNodeStatus        bool
	GetTotalsByInstanceType bool
	GetNodeDetails          bool
}

type Group struct {
	NodeRole     string
	InstanceType string
}

type ClusterMetrics struct {
	// Total contains the summed up metrics
	Totals NodeAllocatedResources `json:"totals,omitempty"`

	Groups []NodeAllocatedResources `json:"groups,omitempty"`

	// Nodes contains allocated resources of each node
	Nodes []NodeAllocatedResources `json:"nodes,omitempty"`
}

// NodeAllocatedResources describes node allocated resources.
type NodeAllocatedResources struct {
	// Node name
	NodeCount int `json:"node_count,omitempty"`

	// Node name
	NodeName string `json:"node_name,omitempty"`

	// The instance Type like m5.2xlarge
	InstanceType string `json:"instance_type,omitempty"`

	// The node role like
	NodeRole string `json:"node_role,omitempty"`

	// CPURequests is number of allocated milicores.
	CPURequests int64 `json:"cpu_requests"`

	// CPURequestsPercentage is the percentage of CPU, that is allocated.
	CPURequestsPercentage float64 `json:"cpu_requests_percentage"`

	// CPULimits is defined CPU limit.
	CPULimits int64 `json:"cpu_limits"`

	// CPULimitsPercentage is a percentage of defined CPU limit, can be over 100%, i.e.
	// overcommitted.
	CPULimitsPercentage float64 `json:"cpu_limits_percentage"`

	// CPUTotal is specified node CPU total in milicores.
	CPUTotal int64 `json:"cpu_total"`

	// MemoryRequests is a percentage of memory, that is allocated.
	MemoryRequests int64 `json:"memory_requests"`

	// MemoryRequestsPercentage is a percentage of memory, that is allocated.
	MemoryRequestsPercentage float64 `json:"memory_requests_percentage"`

	// MemoryLimits is defined memory limit.
	MemoryLimits int64 `json:"memory_limits"`

	// MemoryLimitsPercentage is a percentage of defined memory limit, can be over 100%, i.e.
	// overcommitted.
	MemoryLimitsPercentage float64 `json:"memory_limits_percentage"`

	// MemoryTotal is specified node memory total in bytes.
	MemoryTotal int64 `json:"memory_total"`

	// PodsAllocated in number of currently allocated pods on the node.
	PodsAllocated int `json:"pods_allocated"`

	// PodsTotal is maximum number of pods, that can be allocated on the node.
	PodsTotal int64 `json:"pods_total"`

	// PodsPercentage is a percentage of pods, that can be allocated on given node.
	PodsAllocatedPercentage float64 `json:"pods_allocated_percentage"`

	// Node Labels
	Labels map[string]string `json:"-"`
}

type AllocatedResourcesClient struct {
	kubeClient kubernetes.Interface
	options    *Options
}

func NewAllocatedResourcesClient(kubeClient kubernetes.Interface, options *Options) *AllocatedResourcesClient {
	app := new(AllocatedResourcesClient)
	app.kubeClient = kubeClient
	app.options = options
	return app
}

func getNodeRole(labels map[string]string) string {
	nodeRole := ""
	if _, found := labels[nodeRoleMaster]; found {
		nodeRole = "master"
	}
	if _, found := labels[nodeRoleWorker]; found {
		nodeRole = "worker"
	}
	if _, found := labels[nodeRoleInfra]; found {
		nodeRole = "infra"
	}

	if nodeRole == "" {
		// use OpenShift 3.11 servicecomponent
		nodeRole = labels[serviceComponentLabel]
	}
	return nodeRole
}

func (client *AllocatedResourcesClient) GetAllocatedResources() (ClusterMetrics, error) {
	var nodesAllocatedResources []NodeAllocatedResources
	var totalAllocatedResources NodeAllocatedResources
	var clusterMetrics ClusterMetrics

	groupMap := make(map[Group]NodeAllocatedResources)
	instanceTypeMap := make(map[string]NodeAllocatedResources)

	nodeFieldSelector, err := fields.ParseSelector(client.options.Labels)
	if err != nil {
		return clusterMetrics, err
	}

	nodes, err := client.kubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{LabelSelector: nodeFieldSelector.String()})
	if err != nil {
		return clusterMetrics, err
	}

	for i := range nodes.Items {
		node := nodes.Items[i]
		if client.options.IgnoreNodeStatus || nodeReady(&node) {
			nodeRole := getNodeRole(node.Labels)
			group := Group{nodeRole, node.Labels[instanceTypeLabel]}
			groupMap[group] = NodeAllocatedResources{}

			instanceTypeMap[node.Labels[instanceTypeLabel]] = NodeAllocatedResources{}

			podFieldSelector, err := fields.ParseSelector("spec.nodeName=" + node.Name + ",status.phase!=" + string(v1.PodSucceeded) + ",status.phase!=" + string(v1.PodFailed))
			if err != nil {
				return clusterMetrics, err
			}
			nodeNonTerminatedPodsList, err := client.kubeClient.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{FieldSelector: podFieldSelector.String()})
			if err != nil {
				return clusterMetrics, err
			}

			nodeAllocatedResources, err := getNodeAllocatedResources(node, nodeNonTerminatedPodsList)
			if err != nil {
				return clusterMetrics, err
			}

			nodesAllocatedResources = append(nodesAllocatedResources, nodeAllocatedResources)
		}
	}

	// Calculate totals
	for _, node := range nodesAllocatedResources {
		totalAllocatedResources.CPUTotal += node.CPUTotal
		totalAllocatedResources.CPURequests += node.CPURequests
		totalAllocatedResources.CPULimits += node.CPULimits
		totalAllocatedResources.MemoryTotal += node.MemoryTotal
		totalAllocatedResources.MemoryRequests += node.MemoryRequests
		totalAllocatedResources.MemoryLimits += node.MemoryLimits
		totalAllocatedResources.PodsAllocated += node.PodsAllocated
		totalAllocatedResources.PodsTotal += node.PodsTotal
	}

	if len(nodesAllocatedResources) > 0 {
		totalAllocatedResources.NodeCount = len(nodesAllocatedResources)
		totalAllocatedResources.CPURequestsPercentage = float64(totalAllocatedResources.CPURequests * 100 / totalAllocatedResources.CPUTotal)
		totalAllocatedResources.CPULimitsPercentage = float64(totalAllocatedResources.CPULimits * 100 / totalAllocatedResources.CPUTotal)
		totalAllocatedResources.MemoryRequestsPercentage = float64(totalAllocatedResources.MemoryRequests * 100 / totalAllocatedResources.MemoryTotal)
		totalAllocatedResources.MemoryLimitsPercentage = float64(totalAllocatedResources.MemoryLimits * 100 / totalAllocatedResources.MemoryTotal)
		totalAllocatedResources.PodsAllocatedPercentage = float64(int64(totalAllocatedResources.PodsAllocated) * 100 / totalAllocatedResources.PodsTotal)
	}
	clusterMetrics.Totals = totalAllocatedResources

	// Calculate totals grouped by the instance type
	if client.options.GetTotalsByInstanceType {
		instanceTypeAllocatedResourcesSlice := []NodeAllocatedResources{}

		// summerize resources for each group (node role + instance type combination)
		for _, node := range nodesAllocatedResources {
			nodeRole := getNodeRole(node.Labels)
			group := Group{nodeRole, node.Labels[instanceTypeLabel]}

			if groupAllocatedResources, found := groupMap[group]; found {
				groupAllocatedResources.NodeCount += 1
				groupAllocatedResources.CPUTotal += node.CPUTotal
				groupAllocatedResources.CPURequests += node.CPURequests
				groupAllocatedResources.CPULimits += node.CPULimits
				groupAllocatedResources.MemoryTotal += node.MemoryTotal
				groupAllocatedResources.MemoryRequests += node.MemoryRequests
				groupAllocatedResources.MemoryLimits += node.MemoryLimits
				groupAllocatedResources.PodsAllocated += node.PodsAllocated
				groupAllocatedResources.PodsTotal += node.PodsTotal

				groupMap[group] = groupAllocatedResources
			}
		}

		// set node role, instance role and calculate percentages
		for group, groupAllocatedResources := range groupMap {
			groupAllocatedResources.NodeRole = group.NodeRole
			groupAllocatedResources.InstanceType = group.InstanceType
			groupAllocatedResources.CPURequestsPercentage = float64(groupAllocatedResources.CPURequests * 100 / groupAllocatedResources.CPUTotal)
			groupAllocatedResources.CPULimitsPercentage = float64(groupAllocatedResources.CPULimits * 100 / groupAllocatedResources.CPUTotal)
			groupAllocatedResources.MemoryRequestsPercentage = float64(groupAllocatedResources.MemoryRequests * 100 / groupAllocatedResources.MemoryTotal)
			groupAllocatedResources.MemoryLimitsPercentage = float64(groupAllocatedResources.MemoryLimits * 100 / groupAllocatedResources.MemoryTotal)
			groupAllocatedResources.PodsAllocatedPercentage = float64(int64(groupAllocatedResources.PodsAllocated) * 100 / groupAllocatedResources.PodsTotal)
			instanceTypeAllocatedResourcesSlice = append(instanceTypeAllocatedResourcesSlice, groupAllocatedResources)
		}
		clusterMetrics.Groups = instanceTypeAllocatedResourcesSlice
	}

	// Return node details
	if client.options.GetNodeDetails {
		clusterMetrics.Nodes = nodesAllocatedResources
	}

	return clusterMetrics, nil
}

func podRequestsAndLimits(pod *v1.Pod) (reqs map[v1.ResourceName]resource.Quantity, limits map[v1.ResourceName]resource.Quantity, err error) {
	reqs, limits = map[v1.ResourceName]resource.Quantity{}, map[v1.ResourceName]resource.Quantity{}
	for _, container := range pod.Spec.Containers {
		for name, quantity := range container.Resources.Requests {
			if value, ok := reqs[name]; !ok {
				reqs[name] = *quantity.ToDec()
			} else {
				value.Add(quantity)
				reqs[name] = value
			}
		}
		for name, quantity := range container.Resources.Limits {
			if value, ok := limits[name]; !ok {
				limits[name] = *quantity.ToDec()
			} else {
				value.Add(quantity)
				limits[name] = value
			}
		}
	}
	return
}

func getNodeAllocatedResources(node v1.Node, podList *v1.PodList) (NodeAllocatedResources, error) {
	reqs, limits := map[v1.ResourceName]resource.Quantity{}, map[v1.ResourceName]resource.Quantity{}

	for i := range podList.Items {
		pod := podList.Items[i]
		podReqs, podLimits, err := podRequestsAndLimits(&pod)
		if err != nil {
			return NodeAllocatedResources{}, err
		}
		for podReqName, podReqValue := range podReqs {
			if value, ok := reqs[podReqName]; !ok {
				reqs[podReqName] = *podReqValue.ToDec()
			} else {
				value.Add(podReqValue)
				reqs[podReqName] = value
			}
		}
		for podLimitName, podLimitValue := range podLimits {
			if value, ok := limits[podLimitName]; !ok {
				limits[podLimitName] = *podLimitValue.ToDec()
			} else {
				value.Add(podLimitValue)
				limits[podLimitName] = value
			}
		}
	}

	cpuRequests, cpuLimits, memoryRequests, memoryLimits := reqs[v1.ResourceCPU],
		limits[v1.ResourceCPU], reqs[v1.ResourceMemory], limits[v1.ResourceMemory]

	var cpuRequestsPercentage, cpuLimitsPercentage float64 = 0, 0
	if total := float64(node.Status.Capacity.Cpu().MilliValue()); total > 0 {
		cpuRequestsPercentage = float64(cpuRequests.MilliValue()) / total * 100
		cpuLimitsPercentage = float64(cpuLimits.MilliValue()) / total * 100
	}

	var memoryRequestsPercentage, memoryLimitsPercentage float64 = 0, 0
	if total := float64(node.Status.Capacity.Memory().MilliValue()); total > 0 {
		memoryRequestsPercentage = float64(memoryRequests.MilliValue()) / total * 100
		memoryLimitsPercentage = float64(memoryLimits.MilliValue()) / total * 100
	}

	var podsAllocatedPercentage float64 = 0
	var podsTotal int64 = node.Status.Capacity.Pods().Value()
	if podsTotal > 0 {
		podsAllocatedPercentage = float64(len(podList.Items)) / float64(podsTotal) * 100
	}

	return NodeAllocatedResources{
		Labels:                node.Labels,
		NodeName:              node.Name,
		NodeRole:              getNodeRole(node.Labels),
		InstanceType:          node.Labels[instanceTypeLabel],
		CPUTotal:              node.Status.Capacity.Cpu().MilliValue(),
		CPURequests:           cpuRequests.MilliValue(),
		CPURequestsPercentage: cpuRequestsPercentage,
		CPULimits:             cpuLimits.MilliValue(),
		CPULimitsPercentage:   cpuLimitsPercentage,

		MemoryTotal:              node.Status.Capacity.Memory().Value(),
		MemoryRequests:           memoryRequests.Value(),
		MemoryRequestsPercentage: memoryRequestsPercentage,
		MemoryLimits:             memoryLimits.Value(),
		MemoryLimitsPercentage:   memoryLimitsPercentage,

		PodsAllocated:           len(podList.Items),
		PodsTotal:               podsTotal,
		PodsAllocatedPercentage: podsAllocatedPercentage,
	}, nil
}

func nodeReady(node *v1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == v1.NodeReady && c.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}
