# signalfx-allocated-resources-metrics 

A tool that sends allocated resources metrics to SignalFX. It uses the [kube-allocated-resources](https://gitlab.corp.redhat.com/paas/kube-allocated-resources) library to fetch the metrics from a cluster. 


## Metrics sent to SignalFX

**Datapoints**

| Data Point                   | Description                            |
| ---------------------------- | -------------------------------------- |
| paas.cluster\_node_count      | The total number of nodes             |
| paas.cluster\_memory_total    | The total memory in bytes             |
| paas.cluster\_memory_requests | The summed memory requests in bytes   |
| paas.cluster\_memory_limits   | The summed memory limits in bytes     |
| paas.cluster\_cpu_total       | The total CPU millicores              |
| paas.cluster\_cpu_requests    | The summed CPU requests in millicores |
| paas.cluster\_cpu_limits      | The summed CPU limits in millicores   |

**Dimensions**

| Dimension                          | Description                                 |
| ---------------------------------- | --------------------------------------------|
| node\_kubernetes_io\_node-role     | The node role like master, infra and worker |
| node\_kubernetes_io\_instance-type | The instance type: like m5.2xlarge          |
| node\_kubernetes_io\_instance-size | The instance type: 2xlarge                  |

