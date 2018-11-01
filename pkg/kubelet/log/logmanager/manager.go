package logmanager

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/golang/glog"
	"google.golang.org/grpc"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/logplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/configmap"
	"k8s.io/kubernetes/pkg/kubelet/pod"
	"k8s.io/kubernetes/pkg/kubelet/util/format"
	"k8s.io/kubernetes/pkg/kubelet/volumemanager"
	"k8s.io/kubernetes/pkg/volume/util/volumehelper"
)

type ManagerImpl struct {
	kubeClient clientset.Interface
	recorder   record.EventRecorder
	// logPluginName -> logPluginEndpoint
	mutex      sync.Mutex
	logPlugins map[string]endpoint
	// gRPC server
	socketDir  string
	socketName string
	server     *grpc.Server
	// managers
	podStateManager    *podStateManager
	pluginStateManager *pluginStateManager
	podManager         pod.Manager
	configMapManager   configmap.Manager
	configMapInformer  cache.SharedIndexInformer
	volumeManager      volumemanager.VolumeManager
}

func NewLogPluginManagerImpl(
	kubeClient clientset.Interface,
	recorder record.EventRecorder,
	podManager pod.Manager,
	configMapManager configmap.Manager,
	volumeManager volumemanager.VolumeManager,
) (*ManagerImpl, error) {
	socketDir, socketName := filepath.Split(pluginapi.KubeletSocket)
	m := &ManagerImpl{
		kubeClient:         kubeClient,
		recorder:           recorder,
		logPlugins:         make(map[string]endpoint),
		socketDir:          socketDir,
		socketName:         socketName,
		podStateManager:    newPodStateManager(),
		pluginStateManager: newPluginStateManager(),
		podManager:         podManager,
		configMapManager:   configMapManager,
		volumeManager:      volumeManager,
	}

	m.configMapInformer = cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return kubeClient.CoreV1().ConfigMaps(v1.NamespaceAll).List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return kubeClient.CoreV1().ConfigMaps(v1.NamespaceAll).Watch(options)
			},
		},
		&v1.ConfigMap{},
		resyncPeriod,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	m.configMapInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    nil,
		UpdateFunc: m.onConfigMapUpdate,
		DeleteFunc: nil,
	})

	return m, nil
}

func (m *ManagerImpl) cleanUpDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		filePath := filepath.Join(dir, name)
		stat, err := os.Stat(filePath)
		if err != nil {
			glog.Errorf("failed to stat file %s: %v", filePath, err)
			continue
		}
		if stat.IsDir() {
			continue
		}
		err = os.RemoveAll(filePath)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *ManagerImpl) Start() error {
	glog.V(2).Infof("starting log plugin manager")

	socketPath := filepath.Join(m.socketDir, m.socketName)
	os.MkdirAll(m.socketDir, 0755)

	// Removes all stale sockets in m.socketDir. Log plugins can monitor
	// this and use it as a signal to re-register with the new Kubelet.
	if err := m.cleanUpDir(m.socketDir); err != nil {
		glog.Errorf("fail to clean up stale contents under %s: %+v", m.socketDir, err)
		return err
	}

	s, err := net.Listen("unix", socketPath)
	if err != nil {
		glog.Errorf("server listen error, %v, socket path: %s", err, socketPath)
		return err
	}

	m.server = grpc.NewServer([]grpc.ServerOption{}...)

	pluginapi.RegisterRegistrationServer(m.server, m)
	go m.server.Serve(s)
	glog.V(2).Infof("serving log plugin registration server on %q", socketPath)

	go wait.Until(m.sync, resyncPeriod, wait.NeverStop)
	go m.configMapInformer.Run(wait.NeverStop)

	return nil
}

func filterPods(allPods []*v1.Pod) []*v1.Pod {
	pods := make([]*v1.Pod, 0)
	for _, p := range allPods {
		if isPodLogPolicyExists(p) {
			pods = append(pods, p)
		}
	}
	return pods
}

// ensure consistent between pod log policy and log plugin config
// 1. get all configs from log plugins
// 2. save config states to stateManager
// 3. traversal all pods with log policy, diff configs between log policy configs and log plugin configs
// 4. update configs from log policy to log plugin
func (m *ManagerImpl) sync() {
	// get all pod log configs from log plugins and update inner state
	for logPluginName, endpoint := range m.logPlugins {
		err := m.refreshPluginState(endpoint)
		if err != nil {
			glog.Errorf("pull pod log configs error, %v, endpoint name: %s", err, logPluginName)
			return
		}
	}

	// get all pods with log policy
	pods := filterPods(m.podManager.GetPods())

	// sync pod state to log plugin state
	for _, pod := range pods {
		err := m.refreshPodState(pod)
		if err != nil {
			glog.Errorf("refresh pod log policy error, %v", err)
			continue
		}

		err = m.pushConfigs(pod)
		if err != nil {
			glog.Errorf("push pod log configs error, %v", err)
			continue
		}
	}
}

func (m *ManagerImpl) refreshPluginState(endpoint endpoint) error {
	rsp, err := endpoint.listConfig()
	if err != nil {
		glog.Errorf("list configs from log plugin error, %v", err)
		return err
	}

	m.pluginStateManager.updateAllLogConfigs(rsp.Configs)
	return nil
}

func (m *ManagerImpl) createPodLogDirSymLink(logVolumes logVolumesMap) error {
	// create symlink for log volumes
	for _, logVolume := range logVolumes {
		if err := os.Symlink(logVolume.hostPath, logVolume.logDirPath); err != nil {
			// TODO: need return error?
			glog.Warningf("create symbolic link from %q to %q error, %v", logVolume.logDirPath, logVolume.hostPath, err)
			continue
		}
	}
	return nil
}

//func (m *ManagerImpl) removePodLogDirSymLink(logVolumes logVolumesMap) {
//
//}

// Register registers a log plugin.
func (m *ManagerImpl) Register(ctx context.Context, r *pluginapi.RegisterRequest) (*pluginapi.Empty, error) {
	// glog.Infof("Got registration request from log plugin")
	if r.Version != pluginapi.Version {
		err := fmt.Errorf("invalid version: %s, expected: %s", r.Version, pluginapi.Version)
		glog.Infof("Bad registration request from log plugin, %v", err)
		return &pluginapi.Empty{}, err
	}

	go m.addEndpoint(r)

	return &pluginapi.Empty{}, nil
}

func (m *ManagerImpl) addEndpoint(r *pluginapi.RegisterRequest) {
	glog.Infof("endpoint %q is registering", r.Name)
	socketPath := filepath.Join(pluginapi.LogPluginPath, r.Endpoint)
	e, err := newEndpointImpl(socketPath, r.Name)
	if err != nil {
		glog.Errorf("create endpoint error, %v, log plugin name: %s, socket path: %s", err, r.Name, socketPath)
		return
	}

	m.mutex.Lock()
	oldLogPlugin, exists := m.logPlugins[r.Name]
	if exists && oldLogPlugin != nil {
		oldLogPlugin.stop()
	}
	m.logPlugins[r.Name] = e
	m.mutex.Unlock()
	glog.Infof("endpoint %q is registered, socket path: %s", r.Name, socketPath)
}

func (m *ManagerImpl) buildPodLogVolumes(pod *v1.Pod, podLogPolicy *PodLogPolicy) (logVolumesMap, error) {
	logVolumes := make(logVolumesMap)
	podVolumes := m.volumeManager.GetMountedVolumesForPod(volumehelper.GetUniquePodName(pod))
	for containerName, containerLogPolicies := range podLogPolicy.ContainerLogPolicies {
		for _, containerLogPolicy := range containerLogPolicies {
			if containerLogPolicy.Category == podLogPolicyCategoryStd {
				continue
			}
			volumeInfo, exists := podVolumes[containerLogPolicy.VolumeName]
			if !exists {
				err := fmt.Errorf("%q is not found in podVolumes", containerLogPolicy.VolumeName)
				glog.Error(err)
				return nil, err
			}
			logVolume := &logVolume{
				volumeName: containerLogPolicy.VolumeName,
				path:       containerLogPolicy.Path,
				hostPath:   volumeInfo.Mounter.GetPath(),
				logDirPath: buildLogPolicyDirectory(pod.UID, containerName, containerLogPolicy.Category),
			}
			logVolumes[containerLogPolicy.VolumeName] = logVolume
		}
	}
	return logVolumes, nil
}

func (m *ManagerImpl) buildPodLogConfigMapKeys(pod *v1.Pod, podLogPolicy *PodLogPolicy) (sets.String, error) {
	// configMap key set
	configMapKeys := sets.NewString()
	for _, containerLogPolicies := range podLogPolicy.ContainerLogPolicies {
		for _, containerLogPolicy := range containerLogPolicies {
			// get log config from configmap
			configMap, err := m.configMapManager.GetConfigMap(pod.Namespace, containerLogPolicy.PluginConfigMap)
			if err != nil {
				glog.Errorf("get configmap error, %v, namespace: %s, name: %s", err, pod.Namespace, containerLogPolicy.PluginConfigMap)
				return nil, err
			}
			configMapKeys.Insert(buildConfigMapKey(configMap.Namespace, configMap.Name))
		}
	}
	return configMapKeys, nil
}

func (m *ManagerImpl) buildPodLogConfigs(pod *v1.Pod, podLogPolicy *PodLogPolicy, podLogVolumes logVolumesMap) (logConfigsMap, error) {
	// configName -> PluginLogConfig
	logConfigs := make(logConfigsMap)
	for containerName, containerLogPolicies := range podLogPolicy.ContainerLogPolicies {
		for _, containerLogPolicy := range containerLogPolicies {
			// get log config from configmap
			configMap, err := m.configMapManager.GetConfigMap(pod.Namespace, containerLogPolicy.PluginConfigMap)
			if err != nil {
				glog.Errorf("get configmap error, %v, namespace: %s, name: %s", err, pod.Namespace, containerLogPolicy.PluginConfigMap)
				return nil, err
			}

			var path string
			if containerLogPolicy.Category == podLogPolicyCategoryStd {
				path = buildPodLogsDirectory(pod.UID)
			} else {
				logVolume, exists := podLogVolumes[containerLogPolicy.VolumeName]
				if !exists {
					glog.Errorf("volume is not found in log policy, volume name: %s, pod: %q, log policy: %v, log volumes: %v", containerLogPolicy.VolumeName, format.Pod(pod), podLogPolicy, podLogVolumes)
					continue
				}
				path = logVolume.logDirPath
			}

			// build log config
			for filename, content := range configMap.Data {
				configName := buildLogConfigName(pod.UID, containerName, containerLogPolicy.Category, filename)
				logConfigs[configName] = &pluginapi.Config{
					Metadata: &pluginapi.ConfigMeta{
						Name:          configName,
						PodNamespace:  pod.Namespace,
						PodName:       pod.Name,
						PodUID:        string(pod.UID),
						ContainerName: containerName,
					},
					Spec: &pluginapi.ConfigSpec{
						Content:  content,
						Path:     path,
						Category: containerLogPolicy.Category,
					},
				}
			}
		}
	}
	return logConfigs, nil
}

func (m *ManagerImpl) pushConfigsToPlugin(pod *v1.Pod, endpoint endpoint, logConfigs logConfigsMap) error {
	// diff between logConfigs and podLogPolicyManager.logConfigs
	// generate deleted config name set
	configNames := sets.NewString()
	for configName := range logConfigs {
		configNames.Insert(configName)
	}
	deleted := m.pluginStateManager.getLogConfigNames(pod.UID).Difference(configNames)

	// delete config from log plugin
	for configName := range deleted {
		// invoke log plugin api to add config
		// TODO: rsp
		_, err := endpoint.delConfig(configName)
		if err != nil {
			glog.Errorf("add config to log plugin error, %v, config name: %s, pod: %q", err, configName, format.Pod(pod))
			return err
		}
	}

	// add config to log plugin
	for _, config := range logConfigs {
		// invoke log plugin api to add config
		// TODO: rsp
		_, err := endpoint.addConfig(config)
		if err != nil {
			glog.Errorf("add config to log plugin error, %v, %v", err, config)
			return err
		}
	}

	return nil
}

func (m *ManagerImpl) refreshPodState(pod *v1.Pod) error {
	podLogPolicy, err := getPodLogPolicy(pod)
	if err != nil {
		glog.Errorf("get pod log policy error, %v, pod: %q", err, format.Pod(pod))
		return err
	}

	// create pod log volumes map
	podLogVolumes, err := m.buildPodLogVolumes(pod, podLogPolicy)
	if err != nil {
		glog.Errorf("build pod log volumes error, %v, pod: %q", err, format.Pod(pod))
		return err
	}

	podConfigMapKeys, err := m.buildPodLogConfigMapKeys(pod, podLogPolicy)
	if err != nil {
		glog.Errorf("build pod log configmap keys error, %v, pod: %q", err, format.Pod(pod))
		return err
	}

	m.podStateManager.updateConfigMapKeys(pod.UID, podConfigMapKeys)
	// add log volumes to podLogPolicyManager
	m.podStateManager.updateLogVolumes(pod.UID, podLogVolumes)
	// add log policies to podLogPolicyManager
	m.podStateManager.updateLogPolicy(pod.UID, podLogPolicy)
	return nil
}

func (m *ManagerImpl) pushConfigs(pod *v1.Pod) error {
	logPolicy, exists := m.podStateManager.getLogPolicy(pod.UID)
	if !exists {
		err := fmt.Errorf("log policy not found in state manager, pod: %q", format.Pod(pod))
		glog.Error(err)
		return err
	}

	logVolumes, exists := m.podStateManager.getLogVolumes(pod.UID)
	if !exists {
		err := fmt.Errorf("log volumes not found in state manager, pod: %q", format.Pod(pod))
		glog.Error(err)
		return err
	}

	podLogConfigs, err := m.buildPodLogConfigs(pod, logPolicy, logVolumes)
	if err != nil {
		glog.Errorf("build pod log configs error, %v, pod: %q", err, format.Pod(pod))
		return err
	}

	endpoint, err := m.getLogPluginEndpoint(logPolicy.LogPlugin)
	if err != nil {
		glog.Errorf("get log plugin endpoint error, %v, log plugin name: %s", err, logPolicy.LogPlugin)
		return err
	}

	err = m.pushConfigsToPlugin(pod, endpoint, podLogConfigs)
	if err != nil {
		glog.Errorf("update pod log configs error, %v, pod: %q", err, format.Pod(pod))
		return err
	}

	return nil
}

func (m *ManagerImpl) CreateLogPolicy(pod *v1.Pod) error {
	// ignore pod without log policy
	if !isPodLogPolicyExists(pod) {
		return nil
	}

	err := m.refreshPodState(pod)
	if err != nil {
		glog.Errorf("refresh pod log policy error, %v", err)
		return err
	}

	// ignore check because it must be exists
	logVolumes, _ := m.podStateManager.getLogVolumes(pod.UID)
	// create log symbol link for pod
	err = m.createPodLogDirSymLink(logVolumes)
	if err != nil {
		glog.Errorf("create pod log symbol link error, %v, pod: %q", err, format.Pod(pod))
		return err
	}

	err = m.pushConfigs(pod)
	if err != nil {
		glog.Errorf("push pod log configs error, %v", err)
		return err
	}

	return nil
}

func (m *ManagerImpl) exceedTerminationGracePeriod(pod *v1.Pod) bool {
	// check TerminationGracePeriodSeconds
	if pod.DeletionTimestamp != nil && pod.DeletionGracePeriodSeconds != nil {
		now := time.Now()
		deletionTime := pod.DeletionTimestamp.Time
		gracePeriod := time.Duration(*pod.DeletionGracePeriodSeconds) * time.Second
		if now.After(deletionTime.Add(gracePeriod)) {
			return true
		}
		return false
	}
	return false
}

func (m *ManagerImpl) RemoveLogPolicy(pod *v1.Pod) error {
	// ignore pod without log policy
	if !isPodLogPolicyExists(pod) {
		return nil
	}

	// get log policy from podLogPolicyManager
	podLogPolicy, exists := m.podStateManager.getLogPolicy(pod.UID)
	if !exists {
		glog.Warningf("pod log policy not found, pod: %q", format.Pod(pod))
		return nil
	}

	collectFinished := m.checkCollectFinished(pod.UID, podLogPolicy)

	if podLogPolicy.SafeDeletionEnabled && !collectFinished {
		return fmt.Errorf("log config state is running and SafeDeletionEnabled, cannot remove log policy, pod: %q", format.Pod(pod))
	}

	if m.exceedTerminationGracePeriod(pod) && !collectFinished {
		return fmt.Errorf("log config state is running, cannot remove log policy after grace period seconds, pod: %q", format.Pod(pod))
	}

	endpoint, err := m.getLogPluginEndpoint(podLogPolicy.LogPlugin)
	if err != nil {
		glog.Errorf("get log plugin endpoint error, %v, log plugin name: %s", err, podLogPolicy.LogPlugin)
		return err
	}

	podLogConfigs := make(logConfigsMap)
	err = m.pushConfigsToPlugin(pod, endpoint, podLogConfigs)
	if err != nil {
		glog.Errorf("update pod log configs error, %v, pod: %q", err, format.Pod(pod))
		return err
	}

	m.podStateManager.removeConfigMapKeys(pod.UID)
	// remove log volumes from podLogPolicyManager
	m.podStateManager.removeLogVolumes(pod.UID)
	// remove log policy from podLogPolicyManager
	m.podStateManager.removeLogPolicy(pod.UID)

	return nil
}

func (m *ManagerImpl) checkCollectFinished(podUID k8stypes.UID, podLogPolicy *PodLogPolicy) bool {
	configNames := m.pluginStateManager.getLogConfigNames(podUID)
	if len(configNames) == 0 {
		return true
	}
	endpoint, err := m.getLogPluginEndpoint(podLogPolicy.LogPlugin)
	if err != nil {
		glog.Errorf("get log plugin endpoint error, %v, log plugin name: %s", err, podLogPolicy.LogPlugin)
		return false
	}
	for configName := range configNames {
		rsp, err := endpoint.getState(configName)
		if err != nil {
			glog.Errorf("get state error, %v, config name: %s", err, configName)
			return false
		}
		if rsp.State == pluginapi.State_Running {
			return false
		}
	}
	return true
}

func (m *ManagerImpl) CollectFinished(pod *v1.Pod) bool {
	if !isPodLogPolicyExists(pod) {
		return true
	}

	// get log policy from podLogPolicyManager
	podLogPolicy, exists := m.podStateManager.getLogPolicy(pod.UID)
	if !exists {
		glog.Warningf("pod log policy not found, pod: %q", format.Pod(pod))
		return true
	}

	return m.checkCollectFinished(pod.UID, podLogPolicy)
}

func (m *ManagerImpl) getLogPluginEndpoint(logPluginName string) (endpoint, error) {
	ep, exists := m.logPlugins[logPluginName]
	if !exists {
		return nil, fmt.Errorf("invalid endpoint %s", logPluginName)
	}
	return ep, nil
}

func (m *ManagerImpl) SafeDeletionEnabled(pod *v1.Pod) bool {
	podLogPolicy, exists := m.podStateManager.getLogPolicy(pod.UID)
	if !exists {
		return false
	}
	return podLogPolicy.SafeDeletionEnabled
}

func (m *ManagerImpl) onConfigMapUpdate(oldObj, newObj interface{}) {
	if oldObj.(*v1.ConfigMap).ResourceVersion == newObj.(*v1.ConfigMap).ResourceVersion {
		return
	}
	configMap := newObj.(*v1.ConfigMap)
	configMapKey := buildConfigMapKey(configMap.Namespace, configMap.Name)
	glog.Infof("configMap %q updated", configMapKey)

	// TODO: use work queue
	podUIDs := m.podStateManager.getPodUIDs(configMapKey)
	for podUID := range podUIDs {
		pod, exists := m.podManager.GetPodByUID(k8stypes.UID(podUID))
		if !exists {
			glog.Warningf("pod not found in podManager, pod uid: %s", podUID)
			continue
		}

		podLogPolicy, exists := m.podStateManager.getLogPolicy(pod.UID)
		if !exists {
			glog.Warningf("pod log policy not found, pod: %q", format.Pod(pod))
			continue
		}

		podConfigMapKeys, err := m.buildPodLogConfigMapKeys(pod, podLogPolicy)
		if err != nil {
			glog.Errorf("build pod log configmap key error, %v, pod: %q", err, format.Pod(pod))
			m.recorder.Eventf(pod, v1.EventTypeWarning, LogPolicyConfigUpdateFailedReason, "build pod log configmap keys error, %v", err)
			continue
		}
		m.podStateManager.updateConfigMapKeys(pod.UID, podConfigMapKeys)

		err = m.pushConfigs(pod)
		if err != nil {
			glog.Errorf("push configs error, %v, pod: %q", err, format.Pod(pod))
			m.recorder.Eventf(pod, v1.EventTypeWarning, LogPolicyConfigUpdateFailedReason, "push configs to log plugin error, %v", err)
			continue
		}
	}
}
