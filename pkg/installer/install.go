// This file is part of MinIO Direct CSI
// Copyright (c) 2021 MinIO, Inc.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/minio/direct-csi/pkg/utils"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kerr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var (
	validationWebhookCaBundle []byte
	conversionWebhookCaBundle []byte

	// ErrKubeVersionNotSupported denotes kubernetes version not supported error.
	ErrKubeVersionNotSupported = errors.New(
		utils.Red("Error") +
			"This version of kubernetes is not supported by direct-csi" +
			"Please upgrade your kubernetes installation and try again",
	)

	errEmptyCABundle = errors.New("CA bundle is empty")
)

func errInstallationFailed(reason string, installer string) error {
	return fmt.Errorf("installation failed: %s installer=%s", reason, installer)
}

func objMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      utils.SanitizeKubeResourceName(name),
		Namespace: utils.SanitizeKubeResourceName(name),
		Annotations: map[string]string{
			CreatedByLabel: DirectCSIPluginName,
		},
		Labels: map[string]string{
			"app":  DirectCSI,
			"type": CSIDriver,
		},
	}

}

// CreateNamespace creates direct-csi namespace.
func CreateNamespace(ctx context.Context, identity string, dryRun bool, writer io.Writer) error {
	ns := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: objMeta(identity),
		Spec: corev1.NamespaceSpec{
			Finalizers: []corev1.FinalizerName{},
		},
		Status: corev1.NamespaceStatus{},
	}

	if err := utils.WriteObject(writer, ns); err != nil {
		return err
	}

	if dryRun {
		return utils.LogYAML(ns)
	}

	// Create Namespace Obj
	if _, err := utils.GetKubeClient().CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		return err
	}
	return nil
}

// CreateCSIDriver creates CSI driver.
func CreateCSIDriver(ctx context.Context, identity string, dryRun bool, writer io.Writer) error {
	podInfoOnMount := true
	attachRequired := false

	gvk, err := utils.GetGroupKindVersions("storage.k8s.io", "CSIDriver", "v1", "v1beta1", "v1alpha1")
	if err != nil {
		return err
	}
	version := gvk.Version

	switch version {
	case "v1":
		csiDriver := &storagev1.CSIDriver{
			TypeMeta: metav1.TypeMeta{
				Kind:       "CSIDriver",
				APIVersion: "storage.k8s.io/v1",
			},
			ObjectMeta: objMeta(identity),
			Spec: storagev1.CSIDriverSpec{
				PodInfoOnMount: &podInfoOnMount,
				AttachRequired: &attachRequired,
				VolumeLifecycleModes: []storagev1.VolumeLifecycleMode{
					storagev1.VolumeLifecyclePersistent,
					storagev1.VolumeLifecycleEphemeral,
				},
			},
		}

		if err := utils.WriteObject(writer, csiDriver); err != nil {
			return err
		}

		if dryRun {
			return utils.LogYAML(csiDriver)
		}

		// Create CSIDriver Obj
		if _, err := utils.GetKubeClient().StorageV1().CSIDrivers().Create(ctx, csiDriver, metav1.CreateOptions{}); err != nil {
			return err
		}
	case "v1beta1":
		csiDriver := &storagev1beta1.CSIDriver{
			TypeMeta: metav1.TypeMeta{
				Kind:       "CSIDriver",
				APIVersion: "storage.k8s.io/v1beta1",
			},
			ObjectMeta: objMeta(identity),
			Spec: storagev1beta1.CSIDriverSpec{
				PodInfoOnMount: &podInfoOnMount,
				AttachRequired: &attachRequired,
				VolumeLifecycleModes: []storagev1beta1.VolumeLifecycleMode{
					storagev1beta1.VolumeLifecyclePersistent,
					storagev1beta1.VolumeLifecycleEphemeral,
				},
			},
		}

		if dryRun {
			return utils.LogYAML(csiDriver)
		}

		// Create CSIDriver Obj
		if _, err := utils.GetKubeClient().StorageV1beta1().CSIDrivers().Create(ctx, csiDriver, metav1.CreateOptions{}); err != nil {
			return err
		}
	default:
		return ErrKubeVersionNotSupported
	}
	return nil
}

func getTopologySelectorTerm(identity string) corev1.TopologySelectorTerm {

	getIdentityLabelRequirement := func() corev1.TopologySelectorLabelRequirement {
		return corev1.TopologySelectorLabelRequirement{
			Key:    utils.TopologyDriverIdentity,
			Values: []string{utils.SanitizeKubeResourceName(identity)},
		}
	}

	return corev1.TopologySelectorTerm{
		MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{
			getIdentityLabelRequirement(),
		},
	}
}

// CreateStorageClass creates storage class.
func CreateStorageClass(ctx context.Context, identity string, dryRun bool, writer io.Writer) error {
	allowExpansion := false
	allowedTopologies := []corev1.TopologySelectorTerm{
		getTopologySelectorTerm(identity),
	}
	retainPolicy := corev1.PersistentVolumeReclaimDelete

	gvk, err := utils.GetGroupKindVersions("storage.k8s.io", "CSIDriver", "v1", "v1beta1", "v1alpha1")
	if err != nil {
		return err
	}
	version := gvk.Version

	switch version {
	case "v1":
		bindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		// Create StorageClass for the new driver
		storageClass := &storagev1.StorageClass{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StorageClass",
				APIVersion: "storage.k8s.io/v1",
			},
			ObjectMeta:           objMeta(identity),
			Provisioner:          utils.SanitizeKubeResourceName(identity),
			AllowVolumeExpansion: &allowExpansion,
			VolumeBindingMode:    &bindingMode,
			AllowedTopologies:    allowedTopologies,
			ReclaimPolicy:        &retainPolicy,
			Parameters: map[string]string{
				"fstype": "xfs",
			},
		}
		if err := utils.WriteObject(writer, storageClass); err != nil {
			return err
		}

		if dryRun {
			return utils.LogYAML(storageClass)
		}

		if _, err := utils.GetKubeClient().StorageV1().StorageClasses().Create(ctx, storageClass, metav1.CreateOptions{}); err != nil {
			return err
		}
	case "v1beta1":
		bindingMode := storagev1beta1.VolumeBindingWaitForFirstConsumer
		// Create StorageClass for the new driver
		storageClass := &storagev1beta1.StorageClass{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StorageClass",
				APIVersion: "storage.k8s.io/v1beta1",
			},
			ObjectMeta:           objMeta(identity),
			Provisioner:          utils.SanitizeKubeResourceName(identity),
			AllowVolumeExpansion: &allowExpansion,
			VolumeBindingMode:    &bindingMode,
			AllowedTopologies:    allowedTopologies,
			ReclaimPolicy:        &retainPolicy,
			Parameters: map[string]string{
				"fstype": "xfs",
			},
		}
		if err := utils.WriteObject(writer, storageClass); err != nil {
			return err
		}

		if dryRun {
			return utils.LogYAML(storageClass)
		}

		if _, err := utils.GetKubeClient().StorageV1beta1().StorageClasses().Create(ctx, storageClass, metav1.CreateOptions{}); err != nil {
			return err
		}
	default:
		return ErrKubeVersionNotSupported
	}
	return nil
}

// CreateService creates direct-csi service.
func CreateService(ctx context.Context, identity string, dryRun bool, writer io.Writer) error {
	csiPort := corev1.ServicePort{
		Port: 12345,
		Name: "unused",
	}
	webhookPort := corev1.ServicePort{
		Name: conversionWebhookPortName,
		Port: ConversionWebhookPort,
		TargetPort: intstr.IntOrString{
			Type:   intstr.String,
			StrVal: conversionWebhookPortName,
		},
	}
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: objMeta(identity),
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{csiPort, webhookPort},
			Selector: map[string]string{
				webhookSelector: selectorValueEnabled,
			},
		},
	}
	if err := utils.WriteObject(writer, svc); err != nil {
		return err
	}

	if dryRun {
		return utils.LogYAML(svc)
	}

	if _, err := utils.GetKubeClient().CoreV1().Services(utils.SanitizeKubeResourceName(identity)).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return err
	}
	return nil
}

func getConversionWebhookDNSName(identity string) string {
	return strings.Join([]string{utils.SanitizeKubeResourceName(identity), utils.SanitizeKubeResourceName(identity), "svc"}, ".") // "direct-csi-min-io.direct-csi-min-io.svc"
}

func getConversionHealthzHandler() corev1.Handler {
	return corev1.Handler{
		HTTPGet: &corev1.HTTPGetAction{
			Path:   healthZContainerPortPath,
			Port:   intstr.FromString(conversionWebhookPortName),
			Scheme: corev1.URISchemeHTTPS,
		},
	}
}

func getConversionHealthzURL(identity string) (conversionWebhookURL string) {
	conversionWebhookDNSName := getConversionWebhookDNSName(identity)
	conversionWebhookURL = fmt.Sprintf("https://%s:%d%s", conversionWebhookDNSName, ConversionWebhookPort, healthZContainerPortPath) // https://direct-csi-min-io.direct-csi-min-io.svc:30443/healthz
	return
}

// CreateDaemonSet creates direct-csi daemonset.
func CreateDaemonSet(ctx context.Context,
	identity string,
	directCSIContainerImage string,
	dryRun bool,
	registry, org string,
	loopBackOnly bool,
	nodeSelector map[string]string,
	tolerations []corev1.Toleration,
	seccompProfileName, apparmorProfileName string,
	enableDynamicDiscovery bool,
	writer io.Writer) error {

	name := utils.SanitizeKubeResourceName(identity)
	generatedSelectorValue := generateSanitizedUniqueNameFrom(name)
	conversionHealthzURL := getConversionHealthzURL(identity)

	privileged := true
	securityContext := &corev1.SecurityContext{Privileged: &privileged}

	if seccompProfileName != "" {
		securityContext.SeccompProfile = &corev1.SeccompProfile{
			Type:             corev1.SeccompProfileTypeLocalhost,
			LocalhostProfile: &seccompProfileName,
		}
	}

	volumes := []corev1.Volume{
		newHostPathVolume(volumeNameSocketDir, newDirectCSIPluginsSocketDir(kubeletDirPath, name)),
		newHostPathVolume(volumeNameMountpointDir, kubeletDirPath+"/pods"),
		newHostPathVolume(volumeNameRegistrationDir, kubeletDirPath+"/plugins_registry"),
		newHostPathVolume(volumeNamePluginDir, kubeletDirPath+"/plugins"),
		newHostPathVolume(volumeNameCSIRootDir, csiRootPath),
		newSecretVolume(conversionCACert, conversionCACert),
		newSecretVolume(conversionKeyPair, conversionKeyPair),
	}
	volumeMounts := []corev1.VolumeMount{
		newVolumeMount(volumeNameSocketDir, "/csi", false, false),
		newVolumeMount(volumeNameMountpointDir, kubeletDirPath+"/pods", true, false),
		newVolumeMount(volumeNamePluginDir, kubeletDirPath+"/plugins", true, false),
		newVolumeMount(volumeNameCSIRootDir, csiRootPath, true, false),
		newVolumeMount(conversionCACert, conversionCADir, false, false),
		newVolumeMount(conversionKeyPair, conversionCertsDir, false, false),
	}

	volumes = append(volumes, newHostPathVolume(volumeNameSysDir, volumePathSysDir))
	volumeMounts = append(volumeMounts, newVolumeMount(volumeNameSysDir, volumePathSysDir, true, true))

	if enableDynamicDiscovery {
		volumes = append(volumes, newHostPathVolume(volumeNameDevDir, volumePathDevDir))
		volumeMounts = append(volumeMounts, newVolumeMount(volumeNameDevDir, volumePathDevDir, true, true))

		volumes = append(volumes, newHostPathVolume(volumeNameRunUdevData, volumePathRunUdevData))
		volumeMounts = append(volumeMounts, newVolumeMount(volumeNameRunUdevData, volumePathRunUdevData, true, true))
	}

	podSpec := corev1.PodSpec{
		ServiceAccountName: name,
		HostIPC:            true,
		HostPID:            true,
		Volumes:            volumes,
		Containers: []corev1.Container{
			{
				Name:  nodeDriverRegistrarContainerName,
				Image: filepath.Join(registry, org, CSIImageNodeDriverRegistrar),
				Args: []string{
					fmt.Sprintf("--v=%d", logLevel),
					"--csi-address=unix:///csi/csi.sock",
					fmt.Sprintf("--kubelet-registration-path=%s",
						newDirectCSIPluginsSocketDir(kubeletDirPath, name)+"/csi.sock"),
				},
				Env: []corev1.EnvVar{
					{
						Name: kubeNodeNameEnvVar,
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								APIVersion: "v1",
								FieldPath:  "spec.nodeName",
							},
						},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					newVolumeMount(volumeNameSocketDir, volumePathSocketDir, false, false),
					newVolumeMount(volumeNameRegistrationDir, "/registration", false, false),
				},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				TerminationMessagePath:   "/var/log/driver-registrar-termination-log",
			},
			{
				Name:  directCSIContainerName,
				Image: filepath.Join(registry, org, directCSIContainerImage),
				Args: func() []string {
					args := []string{
						fmt.Sprintf("--identity=%s", name),
						fmt.Sprintf("-v=%d", logLevel),
						fmt.Sprintf("--endpoint=$(%s)", endpointEnvVarCSI),
						fmt.Sprintf("--node-id=$(%s)", kubeNodeNameEnvVar),
						fmt.Sprintf("--conversion-healthz-url=%s", conversionHealthzURL),
						"--driver",
					}
					if loopBackOnly {
						args = append(args, "--loopback-only")
					}
					if enableDynamicDiscovery {
						args = append(args, "--enable-dynamic-discovery")
					}
					return args
				}(),
				SecurityContext: securityContext,
				Env: []corev1.EnvVar{
					{
						Name: kubeNodeNameEnvVar,
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								APIVersion: "v1",
								FieldPath:  "spec.nodeName",
							},
						},
					},
					{
						Name:  endpointEnvVarCSI,
						Value: "unix:///csi/csi.sock",
					},
				},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				TerminationMessagePath:   "/var/log/driver-termination-log",
				VolumeMounts:             volumeMounts,
				Ports: []corev1.ContainerPort{
					{
						ContainerPort: 9898,
						Name:          "healthz",
						Protocol:      corev1.ProtocolTCP,
					},
					{
						ContainerPort: ConversionWebhookPort,
						Name:          conversionWebhookPortName,
						Protocol:      corev1.ProtocolTCP,
					},
				},
				ReadinessProbe: &corev1.Probe{
					Handler: getConversionHealthzHandler(),
				},
				LivenessProbe: &corev1.Probe{
					FailureThreshold:    5,
					InitialDelaySeconds: 300,
					TimeoutSeconds:      5,
					PeriodSeconds:       5,
					Handler: corev1.Handler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: healthZContainerPortPath,
							Port: intstr.FromString(healthZContainerPortName),
						},
					},
				},
			},
			{
				Name:  livenessProbeContainerName,
				Image: filepath.Join(registry, org, CSIImageLivenessProbe),
				Args: []string{
					"--csi-address=/csi/csi.sock",
					"--health-port=9898",
				},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				TerminationMessagePath:   "/var/log/driver-liveness-termination-log",
				VolumeMounts: []corev1.VolumeMount{
					newVolumeMount(volumeNameSocketDir, volumePathSocketDir, false, false),
				},
			},
		},
		NodeSelector: nodeSelector,
		Tolerations:  tolerations,
	}

	if enableDynamicDiscovery {
		podSpec.HostNetwork = true
		podSpec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
	}

	annotations := map[string]string{
		CreatedByLabel: DirectCSIPluginName,
	}
	if apparmorProfileName != "" {
		annotations["container.apparmor.security.beta.kubernetes.io/direct-csi"] = apparmorProfileName
	}

	daemonset := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: objMeta(identity),
		Spec: appsv1.DaemonSetSpec{
			Selector: metav1.AddLabelToSelector(&metav1.LabelSelector{}, directCSISelector, generatedSelectorValue),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        utils.SanitizeKubeResourceName(name),
					Namespace:   utils.SanitizeKubeResourceName(name),
					Annotations: annotations,
					Labels: map[string]string{
						directCSISelector: generatedSelectorValue,
						webhookSelector:   selectorValueEnabled,
					},
				},
				Spec: podSpec,
			},
		},
		Status: appsv1.DaemonSetStatus{},
	}

	if err := utils.WriteObject(writer, daemonset); err != nil {
		return err
	}
	if dryRun {
		return utils.LogYAML(daemonset)
	}

	if _, err := utils.GetKubeClient().AppsV1().DaemonSets(utils.SanitizeKubeResourceName(identity)).Create(ctx, daemonset, metav1.CreateOptions{}); err != nil {
		return err
	}
	return nil
}

// CreateControllerService creates direct-csi controller service.
func CreateControllerService(ctx context.Context, generatedSelectorValue, identity string, dryRun bool) error {
	admissionWebhookPort := corev1.ServicePort{
		Port: admissionControllerWebhookPort,
		TargetPort: intstr.IntOrString{
			Type:   intstr.String,
			StrVal: admissionControllerWebhookName,
		},
	}
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      validationControllerName,
			Namespace: utils.SanitizeKubeResourceName(identity),
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{admissionWebhookPort},
			Selector: map[string]string{
				directCSISelector: generatedSelectorValue,
			},
		},
	}

	if dryRun {
		return utils.LogYAML(svc)
	}

	if _, err := utils.GetKubeClient().CoreV1().Services(utils.SanitizeKubeResourceName(identity)).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return err
	}
	return nil
}

// CreateControllerSecret creates controller secret.
func CreateControllerSecret(ctx context.Context, identity string, publicCertBytes, privateKeyBytes []byte, dryRun bool) error {

	getCertsDataMap := func() map[string][]byte {
		mp := make(map[string][]byte)
		mp[privateKeyFileName] = privateKeyBytes
		mp[publicCertFileName] = publicCertBytes
		return mp
	}

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      admissionWebhookSecretName,
			Namespace: utils.SanitizeKubeResourceName(identity),
		},
		Data: getCertsDataMap(),
	}

	if dryRun {
		return utils.LogYAML(secret)
	}

	if _, err := utils.GetKubeClient().CoreV1().Secrets(utils.SanitizeKubeResourceName(identity)).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return err
	}
	return nil
}

// CreateDeployment creates direct-csi deployment.
func CreateDeployment(ctx context.Context, identity string, directCSIContainerImage string, dryRun bool, registry, org string, writer io.Writer) error {
	name := utils.SanitizeKubeResourceName(identity)
	generatedSelectorValue := generateSanitizedUniqueNameFrom(name)
	conversionHealthzURL := getConversionHealthzURL(identity)

	var replicas int32 = 3
	privileged := true
	podSpec := corev1.PodSpec{
		ServiceAccountName: name,
		Volumes: []corev1.Volume{
			newHostPathVolume(volumeNameSocketDir, newDirectCSIPluginsSocketDir(kubeletDirPath, fmt.Sprintf("%s-controller", name))),
			newSecretVolume(admissionControllerCertsDir, admissionWebhookSecretName),
			newSecretVolume(conversionCACert, conversionCACert),
			newSecretVolume(conversionKeyPair, conversionKeyPair),
		},
		Containers: []corev1.Container{
			{
				Name:  csiProvisionerContainerName,
				Image: filepath.Join(registry, org, CSIImageCSIProvisioner),
				Args: []string{
					fmt.Sprintf("--v=%d", logLevel),
					"--timeout=300s",
					fmt.Sprintf("--csi-address=$(%s)", endpointEnvVarCSI),
					"--leader-election",
					"--feature-gates=Topology=true",
					"--strict-topology",
				},
				Env: []corev1.EnvVar{
					{
						Name:  endpointEnvVarCSI,
						Value: "unix:///csi/csi.sock",
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					newVolumeMount(volumeNameSocketDir, volumePathSocketDir, false, false),
				},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				TerminationMessagePath:   "/var/log/controller-provisioner-termination-log",
				// TODO: Enable this after verification
				// LivenessProbe: &corev1.Probe{
				// 	FailureThreshold:    5,
				// 	InitialDelaySeconds: 10,
				// 	TimeoutSeconds:      3,
				// 	PeriodSeconds:       2,
				// 	Handler: corev1.Handler{
				// 		HTTPGet: &corev1.HTTPGetAction{
				// 			Path: healthZContainerPortPath,
				// 			Port: intstr.FromInt(9898),
				// 		},
				// 	},
				// },
				SecurityContext: &corev1.SecurityContext{
					Privileged: &privileged,
				},
			},
			{
				Name:  directCSIContainerName,
				Image: filepath.Join(registry, org, directCSIContainerImage),
				Args: []string{
					fmt.Sprintf("-v=%d", logLevel),
					fmt.Sprintf("--identity=%s", name),
					fmt.Sprintf("--endpoint=$(%s)", endpointEnvVarCSI),
					fmt.Sprintf("--conversion-healthz-url=%s", conversionHealthzURL),
					"--controller",
				},
				SecurityContext: &corev1.SecurityContext{
					Privileged: &privileged,
				},
				Ports: []corev1.ContainerPort{
					{
						ContainerPort: admissionControllerWebhookPort,
						Name:          admissionControllerWebhookName,
						Protocol:      corev1.ProtocolTCP,
					},
					{
						ContainerPort: 9898,
						Name:          "healthz",
						Protocol:      corev1.ProtocolTCP,
					},
					{
						ContainerPort: ConversionWebhookPort,
						Name:          conversionWebhookPortName,
						Protocol:      corev1.ProtocolTCP,
					},
				},
				ReadinessProbe: &corev1.Probe{
					Handler: getConversionHealthzHandler(),
				},
				Env: []corev1.EnvVar{
					{
						Name: kubeNodeNameEnvVar,
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								APIVersion: "v1",
								FieldPath:  "spec.nodeName",
							},
						},
					},
					{
						Name:  endpointEnvVarCSI,
						Value: "unix:///csi/csi.sock",
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					newVolumeMount(volumeNameSocketDir, volumePathSocketDir, false, false),
					newVolumeMount(admissionControllerCertsDir, admissionCertsDir, false, false),
					newVolumeMount(conversionCACert, conversionCADir, false, false),
					newVolumeMount(conversionKeyPair, conversionCertsDir, false, false),
				},
			},
		},
	}

	caCertBytes, publicCertBytes, privateKeyBytes, certErr := getCerts([]string{admissionWehookDNSName})
	if certErr != nil {
		return certErr
	}
	validationWebhookCaBundle = caCertBytes

	if err := CreateControllerSecret(ctx, identity, publicCertBytes, privateKeyBytes, dryRun); err != nil {
		if !kerr.IsAlreadyExists(err) {
			return err
		}
	}

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: objMeta(identity),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: metav1.AddLabelToSelector(&metav1.LabelSelector{}, directCSISelector, generatedSelectorValue),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      utils.SanitizeKubeResourceName(name),
					Namespace: utils.SanitizeKubeResourceName(name),
					Annotations: map[string]string{
						CreatedByLabel: DirectCSIPluginName,
					},
					Labels: map[string]string{
						directCSISelector: generatedSelectorValue,
						webhookSelector:   selectorValueEnabled,
					},
				},
				Spec: podSpec,
			},
		},
		Status: appsv1.DeploymentStatus{},
	}
	deployment.ObjectMeta.Finalizers = []string{
		utils.SanitizeKubeResourceName(identity) + directCSIFinalizerDeleteProtection,
	}

	if err := utils.WriteObject(writer, deployment); err != nil {
		return err
	}

	if dryRun {
		return utils.LogYAML(deployment)
	}

	if _, err := utils.GetKubeClient().AppsV1().Deployments(utils.SanitizeKubeResourceName(identity)).Create(ctx, deployment, metav1.CreateOptions{}); err != nil {
		return err
	}

	if err := CreateControllerService(ctx, generatedSelectorValue, identity, dryRun); err != nil {
		return err
	}

	return nil
}

func generateSanitizedUniqueNameFrom(name string) string {
	sanitizedName := utils.SanitizeKubeResourceName(name)
	// Max length of name is 255. If needed, cut out last 6 bytes
	// to make room for randomstring
	if len(sanitizedName) >= 255 {
		sanitizedName = sanitizedName[0:249]
	}

	// Get a 5 byte randomstring
	shortUUID := newRandomString(5)

	// Concatenate sanitizedName (249) and shortUUID (5) with a '-' in between
	// Max length of the returned name cannot be more than 255 bytes
	return fmt.Sprintf("%s-%s", sanitizedName, shortUUID)
}

func newHostPathVolume(name, path string) corev1.Volume {
	hostPathType := corev1.HostPathDirectoryOrCreate
	volumeSource := corev1.VolumeSource{
		HostPath: &corev1.HostPathVolumeSource{
			Path: path,
			Type: &hostPathType,
		},
	}

	return corev1.Volume{
		Name:         name,
		VolumeSource: volumeSource,
	}
}

func newSecretVolume(name, secretName string) corev1.Volume {
	volumeSource := corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{
			SecretName: secretName,
		},
	}
	return corev1.Volume{
		Name:         name,
		VolumeSource: volumeSource,
	}
}

func newDirectCSIPluginsSocketDir(kubeletDir, name string) string {
	return filepath.Join(kubeletDir, "plugins", utils.SanitizeKubeResourceName(name))
}

func newVolumeMount(name, path string, bidirectional, readOnly bool) corev1.VolumeMount {
	mountProp := corev1.MountPropagationNone
	if bidirectional {
		mountProp = corev1.MountPropagationBidirectional
	}
	return corev1.VolumeMount{
		Name:             name,
		ReadOnly:         readOnly,
		MountPath:        path,
		MountPropagation: &mountProp,
	}
}

func getDriveValidatingWebhookConfig(identity string) admissionv1.ValidatingWebhookConfiguration {

	name := utils.SanitizeKubeResourceName(identity)
	getServiceRef := func() *admissionv1.ServiceReference {
		path := "/validatedrive"
		return &admissionv1.ServiceReference{
			Namespace: name,
			Name:      validationControllerName,
			Path:      &path,
		}
	}

	getClientConfig := func() admissionv1.WebhookClientConfig {
		return admissionv1.WebhookClientConfig{
			Service:  getServiceRef(),
			CABundle: []byte(validationWebhookCaBundle),
		}

	}

	getValidationRules := func() []admissionv1.RuleWithOperations {
		return []admissionv1.RuleWithOperations{
			{
				Operations: []admissionv1.OperationType{admissionv1.Update},
				Rule: admissionv1.Rule{
					APIGroups:   []string{"*"},
					APIVersions: []string{"*"},
					Resources:   []string{"directcsidrives"},
				},
			},
		}
	}

	getValidatingWebhooks := func() []admissionv1.ValidatingWebhook {
		supportedReviewVersions := []string{"v1", "v1beta1", "v1beta2", "v1beta3"}
		sideEffectClass := admissionv1.SideEffectClassNone
		return []admissionv1.ValidatingWebhook{
			{
				Name:                    validationWebhookConfigName,
				ClientConfig:            getClientConfig(),
				AdmissionReviewVersions: supportedReviewVersions,
				SideEffects:             &sideEffectClass,
				Rules:                   getValidationRules(),
			},
		}
	}

	validatingWebhookConfiguration := admissionv1.ValidatingWebhookConfiguration{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ValidatingWebhookConfiguration",
			APIVersion: "admissionregistration.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      validationWebhookConfigName,
			Namespace: name,
			Finalizers: []string{
				utils.SanitizeKubeResourceName(identity) + directCSIFinalizerDeleteProtection,
			},
		},
		Webhooks: getValidatingWebhooks(),
	}

	return validatingWebhookConfiguration
}

// RegisterDriveValidationRules registers drive validation rules.
func RegisterDriveValidationRules(ctx context.Context, identity string, dryRun bool, writer io.Writer) error {
	driveValidatingWebhookConfig := getDriveValidatingWebhookConfig(identity)
	if err := utils.WriteObject(writer, driveValidatingWebhookConfig); err != nil {
		return err
	}
	if dryRun {
		return utils.LogYAML(driveValidatingWebhookConfig)
	}

	if _, err := utils.GetKubeClient().
		AdmissionregistrationV1().
		ValidatingWebhookConfigurations().
		Create(ctx, &driveValidatingWebhookConfig, metav1.CreateOptions{}); err != nil {

		return err
	}
	return nil
}

// CreateOrUpdateConversionKeyPairSecret creates/updates conversion keypairs secret.
func CreateOrUpdateConversionKeyPairSecret(ctx context.Context, identity string, publicCertBytes, privateKeyBytes []byte, dryRun bool, writer io.Writer) error {

	secretsClient := utils.GetKubeClient().CoreV1().Secrets(utils.SanitizeKubeResourceName(identity))

	getCertsDataMap := func() map[string][]byte {
		mp := make(map[string][]byte)
		mp[privateKeyFileName] = privateKeyBytes
		mp[publicCertFileName] = publicCertBytes
		return mp
	}

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      conversionKeyPair,
			Namespace: utils.SanitizeKubeResourceName(identity),
		},
		Data: getCertsDataMap(),
	}

	if err := utils.WriteObject(writer, secret); err != nil {
		return err
	}

	if dryRun {
		return utils.LogYAML(secret)
	}

	existingSecret, err := secretsClient.Get(ctx, conversionKeyPair, metav1.GetOptions{})
	if err != nil {
		if !kerr.IsNotFound(err) {
			return err
		}
		if _, err := secretsClient.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return err
		}
		return nil
	}

	existingSecret.Data = secret.Data
	if _, err := secretsClient.Update(ctx, existingSecret, metav1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}

// CreateOrUpdateConversionCACertSecret creates/updates conversion CA certs secret.
func CreateOrUpdateConversionCACertSecret(ctx context.Context, identity string, caCertBytes []byte, dryRun bool, writer io.Writer) error {

	secretsClient := utils.GetKubeClient().CoreV1().Secrets(utils.SanitizeKubeResourceName(identity))

	getCertsDataMap := func() map[string][]byte {
		mp := make(map[string][]byte)
		mp[caCertFileName] = caCertBytes
		return mp
	}

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      conversionCACert,
			Namespace: utils.SanitizeKubeResourceName(identity),
		},
		Data: getCertsDataMap(),
	}
	if err := utils.WriteObject(writer, secret); err != nil {
		return err
	}
	if dryRun {
		return utils.LogYAML(secret)
	}

	existingSecret, err := secretsClient.Get(ctx, conversionCACert, metav1.GetOptions{})
	if err != nil {
		if !kerr.IsNotFound(err) {
			return err
		}
		if _, err := secretsClient.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return err
		}
		return nil
	}

	existingSecret.Data = secret.Data
	if _, err := secretsClient.Update(ctx, existingSecret, metav1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}

// GetConversionCABundle gets conversion CA bundle.
func GetConversionCABundle(ctx context.Context, identity string, dryRun bool) ([]byte, error) {
	getCABundlerFromGlobal := func() ([]byte, error) {
		if len(conversionWebhookCaBundle) == 0 {
			return []byte{}, errEmptyCABundle
		}
		return conversionWebhookCaBundle, nil
	}

	secret, err := utils.GetKubeClient().
		CoreV1().
		Secrets(utils.SanitizeKubeResourceName(identity)).
		Get(ctx, conversionCACert, metav1.GetOptions{})
	if err != nil {
		if kerr.IsNotFound(err) && dryRun {
			return getCABundlerFromGlobal()
		}
		return []byte{}, err
	}

	for key, value := range secret.Data {
		if key == caCertFileName {
			return value, nil
		}
	}

	return []byte{}, errEmptyCABundle
}

func checkConversionSecrets(ctx context.Context, identity string) error {
	secretsClient := utils.GetKubeClient().CoreV1().Secrets(utils.SanitizeKubeResourceName(identity))
	if _, err := secretsClient.Get(ctx, conversionKeyPair, metav1.GetOptions{}); err != nil {
		return err
	}
	_, err := secretsClient.Get(ctx, conversionCACert, metav1.GetOptions{})
	return err
}

// CreateConversionWebhookSecrets creates conversion webhook secrets.
func CreateConversionWebhookSecrets(ctx context.Context, identity string, dryRun bool, writer io.Writer) error {

	err := checkConversionSecrets(ctx, identity)
	if err == nil {
		return nil
	}
	if !kerr.IsNotFound(err) {
		return err
	}

	caCertBytes, publicCertBytes, privateKeyBytes, certErr := getCerts([]string{getConversionWebhookDNSName(identity)})
	if certErr != nil {
		return certErr
	}
	conversionWebhookCaBundle = caCertBytes

	if err := CreateOrUpdateConversionKeyPairSecret(ctx, identity, publicCertBytes, privateKeyBytes, dryRun, writer); err != nil {
		return err
	}

	return CreateOrUpdateConversionCACertSecret(ctx, identity, caCertBytes, dryRun, writer)
}
