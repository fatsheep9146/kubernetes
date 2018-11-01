package logmanager

import (
	"time"

	"k8s.io/api/core/v1"
)

const (
	podLogPolicyLabelKey = "alpha.log.qiniu.com/log-policy"
	// The root directory for pod logs
	podLogsRootDirectory    = "/var/log/pods"
	resyncPeriod            = 3 * time.Minute
	podLogPolicyCategoryStd = "std"
)

// log policy event reason
const (
	LogPolicyConfigUpdateFailedReason = "LogPolicyConfigUpdateFailed"
)

type PodLogPolicy struct {
	// log plugin name, eg. logkit, logexporter
	LogPlugin string `json:"log_plugin"`
	// ensure all log been collected before pod are terminated
	// SafeDeletionEnabled == true, pod will keep terminating forever util log plugin says all log are collected.
	// SafeDeletionEnabled == false, pod will terminated before TerminationGracePeriodSeconds in PodSpec.
	SafeDeletionEnabled bool `json:"safe_deletion_enabled"`
	// container name -> ContainerLogPolicies
	ContainerLogPolicies map[string]ContainerLogPolicies `json:"container_log_policies"`
}

type ContainerLogPolicies []*ContainerLogPolicy

type ContainerLogPolicy struct {
	// log category, eg. std(stdout/stderr), app, audit
	// if category is "std", path and volume_name will make no sense
	Category string `json:"category"`
	// log volume mount path
	Path string `json:"path"`
	// volume(mount) name
	// volume for container log
	VolumeName string `json:"volume_name"`
	// configmap name of log plugin configs
	PluginConfigMap string `json:"plugin_configmap"`
}

//type PluginLogConfigState string
//
//const (
//	PluginLogConfigStatePending PluginLogConfigState = "Pending"
//	PluginLogConfigStateRunning PluginLogConfigState = "Running"
//	PluginLogConfigStateIdle    PluginLogConfigState = "Idle"
//)

type Manager interface {
	CreateLogPolicy(pod *v1.Pod) error
	RemoveLogPolicy(pod *v1.Pod) error
	CollectFinished(pod *v1.Pod) bool
	Start() error
}
