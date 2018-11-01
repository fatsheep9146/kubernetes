package logmanager

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/golang/glog"
	"k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

func buildConfigMapKey(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// buildPodLogsDirectory builds absolute log directory path for a pod.
func buildPodLogsDirectory(podUID k8stypes.UID) string {
	return filepath.Join(podLogsRootDirectory, string(podUID))
}

func buildLogPolicyDirectory(podUID k8stypes.UID, containerName string, category string) string {
	return filepath.Join(buildPodLogsDirectory(podUID), containerName, category)
}

func buildLogConfigName(podUID k8stypes.UID, containerName string, category string, filename string) string {
	// <pod-uid>/<container_name>/<category>/<name>
	return fmt.Sprintf("%s/%s/%s/%s", podUID, containerName, category, filename)
}

func isPodLogPolicyExists(pod *v1.Pod) bool {
	_, exists := pod.Annotations[podLogPolicyLabelKey]
	if !exists {
		return false
	}
	return true
}

func getPodLogPolicy(pod *v1.Pod) (*PodLogPolicy, error) {
	// get log policy from pod annotations
	podLogPolicyLabelValue := pod.Annotations[podLogPolicyLabelKey]
	podLogPolicy := &PodLogPolicy{}
	err := json.Unmarshal([]byte(podLogPolicyLabelValue), podLogPolicy)
	if err != nil {
		glog.Errorf("json unmarshal error, %v, podLogPolicyLabelValue: %s", err, podLogPolicyLabelValue)
		return nil, err
	}
	return podLogPolicy, nil
}
