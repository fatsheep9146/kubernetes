package container

import (
	"testing"

	"k8s.io/kubernetes/pkg/util/hash"
)

func TestHackContainerHash(t *testing.T) {
	testcases := []struct {
		original string
		expected string
		hackFunc hash.HackFunc
	}{
		{
			`(v1.Container){Name:(string)kube-proxy Image:(string)index-dev.qiniu.io/kelibrary/kube-proxy-amd64:v1.8.13 Command:([]string)[/usr/local/bin/kube-proxy --config=/var/lib/kube-proxy/config.conf] Args:([]string)<nil> WorkingDir:(string) Ports:([]v1.ContainerPort)<nil> EnvFrom:([]v1.EnvFromSource)<nil> Env:([]v1.EnvVar)<nil> Resources:(v1.ResourceRequirements){Limits:(v1.ResourceList)<nil> Requests:(v1.ResourceList)<nil>} VolumeMounts:([]v1.VolumeMount)[{Name:(string)kube-proxy ReadOnly:(bool)false MountPath:(string)/var/lib/kube-proxy SubPath:(string) MountPropagation:(*v1.MountPropagationMode)<nil>} {Name:(string)xtables-lock ReadOnly:(bool)false MountPath:(string)/run/xtables.lock SubPath:(string) MountPropagation:(*v1.MountPropagationMode)<nil>} {Name:(string)lib-modules ReadOnly:(bool)true MountPath:(string)/lib/modules SubPath:(string) MountPropagation:(*v1.MountPropagationMode)<nil>} {Name:(string)kube-proxy-token-jdlsl ReadOnly:(bool)true MountPath:(string)/var/run/secrets/kubernetes.io/serviceaccount SubPath:(string) MountPropagation:(*v1.MountPropagationMode)<nil>}] LivenessProbe:(*v1.Probe)<nil> ReadinessProbe:(*v1.Probe)<nil> Lifecycle:(*v1.Lifecycle)<nil> TerminationMessagePath:(string)/dev/termination-log TerminationMessagePolicy:(v1.TerminationMessagePolicy)File ImagePullPolicy:(v1.PullPolicy)IfNotPresent SecurityContext:(*v1.SecurityContext){Capabilities:(*v1.Capabilities)<nil> Privileged:(*bool)true SELinuxOptions:(*v1.SELinuxOptions)<nil> RunAsUser:(*int64)<nil> RunAsNonRoot:(*bool)<nil> ReadOnlyRootFilesystem:(*bool)<nil> AllowPrivilegeEscalation:(*bool)<nil>} Stdin:(bool)false StdinOnce:(bool)false TTY:(bool)false}`,
			`(v1.Container){Name:(string)kube-proxy Image:(string)index-dev.qiniu.io/kelibrary/kube-proxy-amd64:v1.7.3 Command:([]string)[/usr/local/bin/kube-proxy --config=/var/lib/kube-proxy/config.conf] Args:([]string)<nil> WorkingDir:(string) Ports:([]v1.ContainerPort)<nil> EnvFrom:([]v1.EnvFromSource)<nil> Env:([]v1.EnvVar)<nil> Resources:(v1.ResourceRequirements){Limits:(v1.ResourceList)<nil> Requests:(v1.ResourceList)<nil>} VolumeMounts:([]v1.VolumeMount)[{Name:(string)kube-proxy ReadOnly:(bool)false MountPath:(string)/var/lib/kube-proxy SubPath:(string)} {Name:(string)xtables-lock ReadOnly:(bool)false MountPath:(string)/run/xtables.lock SubPath:(string)} {Name:(string)lib-modules ReadOnly:(bool)true MountPath:(string)/lib/modules SubPath:(string)} {Name:(string)kube-proxy-token-jdlsl ReadOnly:(bool)true MountPath:(string)/var/run/secrets/kubernetes.io/serviceaccount SubPath:(string)}] LivenessProbe:(*v1.Probe)<nil> ReadinessProbe:(*v1.Probe)<nil> Lifecycle:(*v1.Lifecycle)<nil> TerminationMessagePath:(string)/dev/termination-log TerminationMessagePolicy:(v1.TerminationMessagePolicy)File ImagePullPolicy:(v1.PullPolicy)IfNotPresent SecurityContext:(*v1.SecurityContext){Capabilities:(*v1.Capabilities)<nil> Privileged:(*bool)true SELinuxOptions:(*v1.SELinuxOptions)<nil> RunAsUser:(*int64)<nil> RunAsNonRoot:(*bool)<nil> ReadOnlyRootFilesystem:(*bool)<nil>} Stdin:(bool)false StdinOnce:(bool)false TTY:(bool)false}`,
			HackContainerHashTo17,
		},
	}

	for _, tc := range testcases {
		actual := tc.hackFunc(tc.original)
		if actual != tc.expected {
			t.Errorf("hack container hash error, expected: \n %s\nactual: \n%s\n", tc.expected, actual)
		}
	}
}
