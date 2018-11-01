package logmanager

import (
	"context"
	"fmt"
	"github.com/golang/glog"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/logplugin/v1alpha1"
	"net"
	"sync"
	"time"
)

// endpointStopGracePeriod indicates the grace period after an endpoint is stopped
// because its log plugin fails. LogPluginManager keeps the stopped endpoint in its
// cache during this grace period to cover the time gap for the capacity change to
// take effect.
const endpointStopGracePeriod = time.Duration(5) * time.Minute

// endpoint maps to a single registered log plugin. It is responsible
// for managing gRPC communications with the log plugin and caching
// states reported by the log plugin.
type endpoint interface {
	run()
	stop()

	addConfig(config *pluginapi.Config) (*pluginapi.AddConfigResponse, error)
	delConfig(name string) (*pluginapi.DelConfigResponse, error)
	listConfig() (*pluginapi.ListConfigResponse, error)
	getState(name string) (*pluginapi.GetStateResponse, error)
}

type endpointImpl struct {
	client     pluginapi.LogPluginClient
	clientConn *grpc.ClientConn
	stopTime   time.Time
	mutex      sync.Mutex

	socketPath    string
	logPluginName string
}

// newEndpoint creates a new endpoint for the given resourceName.
// This is to be used during normal device plugin registration.
func newEndpointImpl(socketPath, logPluginName string) (*endpointImpl, error) {
	client, c, err := dial(socketPath)
	if err != nil {
		glog.Errorf("Can't create new endpoint with path %s err %v", socketPath, err)
		return nil, err
	}

	return &endpointImpl{
		client:        client,
		clientConn:    c,
		socketPath:    socketPath,
		logPluginName: logPluginName,
	}, nil
}

// newStoppedEndpointImpl creates a new endpoint for the given logPluginName with stopTime set.
// This is to be used during Kubelet restart, before the actual device plugin re-registers.
func newStoppedEndpointImpl(logPluginName string) *endpointImpl {
	return &endpointImpl{
		logPluginName: logPluginName,
		stopTime:      time.Now(),
	}
}

func (e *endpointImpl) isStopped() bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return !e.stopTime.IsZero()
}

func (e *endpointImpl) stopGracePeriodExpired() bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return !e.stopTime.IsZero() && time.Since(e.stopTime) > endpointStopGracePeriod
}

// used for testing only
func (e *endpointImpl) setStopTime(t time.Time) {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	e.stopTime = t
}

func (e *endpointImpl) run() {

}

func (e *endpointImpl) stop() {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	if e.clientConn != nil {
		e.clientConn.Close()
	}
	e.stopTime = time.Now()
}

// dial establishes the gRPC communication with the registered device plugin.
func dial(unixSocketPath string) (pluginapi.LogPluginClient, *grpc.ClientConn, error) {
	c, err := grpc.Dial(unixSocketPath, grpc.WithInsecure(),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)

	if err != nil {
		return nil, nil, fmt.Errorf("dial to %s error, %v", unixSocketPath, err)
	}

	return pluginapi.NewLogPluginClient(c), c, nil
}

func (e *endpointImpl) addConfig(config *pluginapi.Config) (*pluginapi.AddConfigResponse, error) {
	if e.isStopped() {
		return nil, fmt.Errorf("endpoint %s is stopped", e.logPluginName)
	}
	return e.client.AddConfig(context.Background(), &pluginapi.AddConfigRequest{
		Config: config,
	})
}

func (e *endpointImpl) delConfig(name string) (*pluginapi.DelConfigResponse, error) {
	if e.isStopped() {
		return nil, fmt.Errorf("endpoint %s is stopped", e.logPluginName)
	}
	return e.client.DelConfig(context.Background(), &pluginapi.DelConfigRequest{
		Name: name,
	})
}

func (e *endpointImpl) listConfig() (*pluginapi.ListConfigResponse, error) {
	if e.isStopped() {
		return nil, fmt.Errorf("endpoint %s is stopped", e.logPluginName)
	}
	return e.client.ListConfig(context.Background(), &pluginapi.Empty{})
}

func (e *endpointImpl) getState(name string) (*pluginapi.GetStateResponse, error) {
	if e.isStopped() {
		return nil, fmt.Errorf("endpoint %s is stopped", e.logPluginName)
	}
	return e.client.GetState(context.Background(), &pluginapi.GetStateRequest{
		Name: name,
	})
}
