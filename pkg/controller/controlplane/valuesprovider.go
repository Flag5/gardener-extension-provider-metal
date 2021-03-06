package controlplane

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/gardener/gardener-extensions/pkg/util"
	"github.com/metal-stack/metal-lib/pkg/tag"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"path/filepath"

	extensionscontroller "github.com/gardener/gardener-extensions/pkg/controller"
	"github.com/gardener/gardener-extensions/pkg/controller/controlplane"
	"github.com/gardener/gardener-extensions/pkg/controller/controlplane/genericactuator"
	gardenerkubernetes "github.com/gardener/gardener/pkg/client/kubernetes"
	apismetal "github.com/metal-stack/gardener-extension-provider-metal/pkg/apis/metal"
	"github.com/metal-stack/gardener-extension-provider-metal/pkg/apis/metal/helper"

	metalclient "github.com/metal-stack/gardener-extension-provider-metal/pkg/metal/client"
	metalgo "github.com/metal-stack/metal-go"

	"github.com/metal-stack/gardener-extension-provider-metal/pkg/metal"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	kutil "github.com/gardener/gardener/pkg/utils/kubernetes"

	v1alpha1constants "github.com/gardener/gardener/pkg/apis/core/v1alpha1/constants"

	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	"github.com/gardener/gardener/pkg/utils/chart"
	"github.com/gardener/gardener/pkg/utils/secrets"

	"github.com/go-logr/logr"

	"github.com/pkg/errors"

	admissionv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Object names
const (
	cloudControllerManagerDeploymentName = "cloud-controller-manager"
	cloudControllerManagerServerName     = "cloud-controller-manager-server"
	groupRolebindingControllerName       = "group-rolebinding-controller"
	limitValidatingWebhookDeploymentName = "limit-validating-webhook"
	limitValidatingWebhookServerName     = "limit-validating-webhook-server"
	accountingExporterName               = "accounting-exporter"
	authNWebhookDeploymentName           = "kube-jwt-authn-webhook"
	authNWebhookServerName               = "kube-jwt-authn-webhook-server"
	droptailerNamespace                  = "firewall"
	droptailerClientSecretName           = "droptailer-client"
	droptailerServerSecretName           = "droptailer-server"
)

var controlPlaneSecrets = &secrets.Secrets{
	CertificateSecretConfigs: map[string]*secrets.CertificateSecretConfig{
		v1alpha1constants.SecretNameCACluster: {
			Name:       v1alpha1constants.SecretNameCACluster,
			CommonName: "kubernetes",
			CertType:   secrets.CACert,
		},
	},
	SecretConfigsFunc: func(cas map[string]*secrets.Certificate, clusterName string) []secrets.ConfigInterface {
		return []secrets.ConfigInterface{
			&secrets.ControlPlaneSecretConfig{
				CertificateSecretConfig: &secrets.CertificateSecretConfig{
					Name:         cloudControllerManagerDeploymentName,
					CommonName:   "system:cloud-controller-manager",
					Organization: []string{user.SystemPrivilegedGroup},
					CertType:     secrets.ClientCert,
					SigningCA:    cas[v1alpha1constants.SecretNameCACluster],
				},
				KubeConfigRequest: &secrets.KubeConfigRequest{
					ClusterName:  clusterName,
					APIServerURL: v1alpha1constants.DeploymentNameKubeAPIServer,
				},
			},
			&secrets.ControlPlaneSecretConfig{
				CertificateSecretConfig: &secrets.CertificateSecretConfig{
					Name:         groupRolebindingControllerName,
					CommonName:   "system:group-rolebinding-controller",
					Organization: []string{user.SystemPrivilegedGroup},
					CertType:     secrets.ClientCert,
					SigningCA:    cas[v1alpha1constants.SecretNameCACluster],
				},
				KubeConfigRequest: &secrets.KubeConfigRequest{
					ClusterName:  clusterName,
					APIServerURL: v1alpha1constants.DeploymentNameKubeAPIServer,
				},
			},
			&secrets.ControlPlaneSecretConfig{
				CertificateSecretConfig: &secrets.CertificateSecretConfig{
					Name:       authNWebhookServerName,
					CommonName: authNWebhookDeploymentName,
					DNSNames:   controlplane.DNSNamesForService(authNWebhookDeploymentName, clusterName),
					CertType:   secrets.ServerCert,
					SigningCA:  cas[v1alpha1constants.SecretNameCACluster],
				},
			},
			&secrets.ControlPlaneSecretConfig{
				CertificateSecretConfig: &secrets.CertificateSecretConfig{
					Name:       limitValidatingWebhookServerName,
					CommonName: limitValidatingWebhookDeploymentName,
					DNSNames:   controlplane.DNSNamesForService(limitValidatingWebhookDeploymentName, clusterName),
					CertType:   secrets.ServerCert,
					SigningCA:  cas[v1alpha1constants.SecretNameCACluster],
				},
			},
			&secrets.ControlPlaneSecretConfig{
				CertificateSecretConfig: &secrets.CertificateSecretConfig{
					Name:       accountingExporterName,
					CommonName: "system:accounting-exporter",
					// Groupname of user
					Organization: []string{accountingExporterName},
					CertType:     secrets.ClientCert,
					SigningCA:    cas[v1alpha1constants.SecretNameCACluster],
				},
				KubeConfigRequest: &secrets.KubeConfigRequest{
					ClusterName:  clusterName,
					APIServerURL: v1alpha1constants.DeploymentNameKubeAPIServer,
				},
			},
			&secrets.ControlPlaneSecretConfig{
				CertificateSecretConfig: &secrets.CertificateSecretConfig{
					Name:       cloudControllerManagerServerName,
					CommonName: cloudControllerManagerDeploymentName,
					DNSNames:   controlplane.DNSNamesForService(cloudControllerManagerDeploymentName, clusterName),
					CertType:   secrets.ServerCert,
					SigningCA:  cas[v1alpha1constants.SecretNameCACluster],
				},
			},
		}
	},
}

var configChart = &chart.Chart{
	Name:   "config",
	Path:   filepath.Join(metal.InternalChartsPath, "cloud-provider-config"),
	Images: []string{},
	Objects: []*chart.Object{
		// this config is mounted by the shoot-kube-apiserver at startup and should therefore be deployed before the controlplane
		{Type: &corev1.ConfigMap{}, Name: "authn-webhook-config"},
	},
}

var controlPlaneChart = &chart.Chart{
	Name:   "control-plane",
	Path:   filepath.Join(metal.InternalChartsPath, "control-plane"),
	Images: []string{metal.CCMImageName, metal.AuthNWebhookImageName, metal.AccountingExporterImageName, metal.GroupRolebindingControllerImageName, metal.LimitValidatingWebhookImageName},
	Objects: []*chart.Object{
		// cloud controller manager
		{Type: &corev1.Service{}, Name: "cloud-controller-manager"},
		{Type: &appsv1.Deployment{}, Name: "cloud-controller-manager"},

		// authn webhook
		{Type: &appsv1.Deployment{}, Name: "kube-jwt-authn-webhook"},
		{Type: &corev1.Service{}, Name: "kube-jwt-authn-webhook"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "kubeapi2kube-jwt-authn-webhook"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "kube-jwt-authn-webhook-allow-namespace"},

		// accounting exporter
		{Type: &appsv1.Deployment{}, Name: "accounting-exporter"},
		{Type: &rbacv1.RoleBinding{}, Name: "accounting-exporter"},
		{Type: &rbacv1.Role{}, Name: "accounting-exporter"},

		// group rolebinding controller
		{Type: &appsv1.Deployment{}, Name: "group-rolebinding-controller"},

		// limit validation webhook
		{Type: &appsv1.Deployment{}, Name: "limit-validating-webhook"},
		{Type: &corev1.Service{}, Name: "limit-validating-webhook"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "limit-validating-webhook-allow-namespace"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "kubeapi2limit-validating-webhook"},

		// network policies
		{Type: &networkingv1.NetworkPolicy{}, Name: "egress-allow-dns"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "egress-allow-any"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "egress-allow-https"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "egress-allow-ntp"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "egress-allow-vpn"},
	},
}

var cpShootChart = &chart.Chart{
	Name:   "shoot-control-plane",
	Path:   filepath.Join(metal.InternalChartsPath, "shoot-control-plane"),
	Images: []string{metal.DroptailerImageName, metal.MetallbSpeakerImageName, metal.MetallbControllerImageName},
	Objects: []*chart.Object{
		// limit validating webhook
		{Type: &admissionv1beta1.ValidatingWebhookConfiguration{}, Name: "limit-validating-webhook"},

		// metallb
		{Type: &corev1.Namespace{}, Name: "metallb-system"},
		{Type: &policyv1beta1.PodSecurityPolicy{}, Name: "speaker"},
		{Type: &corev1.ServiceAccount{}, Name: "controller"},
		{Type: &corev1.ServiceAccount{}, Name: "speaker"},
		{Type: &rbacv1.ClusterRole{}, Name: "metallb-system:controller"},
		{Type: &rbacv1.ClusterRole{}, Name: "metallb-system:speaker"},
		{Type: &rbacv1.Role{}, Name: "config-watcher"},
		{Type: &rbacv1.ClusterRoleBinding{}, Name: "metallb-system:controller"},
		{Type: &rbacv1.ClusterRoleBinding{}, Name: "metallb-system:speaker"},
		{Type: &rbacv1.RoleBinding{}, Name: "config-watcher"},
		{Type: &appsv1.DaemonSet{}, Name: "speaker"},
		{Type: &appsv1.Deployment{}, Name: "controller"},

		// network policies
		{Type: &networkingv1.NetworkPolicy{}, Name: "egress-allow-dns"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "egress-allow-any"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "egress-allow-https"},
		{Type: &networkingv1.NetworkPolicy{}, Name: "egress-allow-ntp"},

		// accounting controller
		{Type: &rbacv1.ClusterRole{}, Name: "system:accounting-exporter"},
		{Type: &rbacv1.ClusterRoleBinding{}, Name: "system:accounting-exporter"},

		// firewall controller
		{Type: &rbacv1.ClusterRole{}, Name: "system:firewall-policy-controller"},
		{Type: &rbacv1.ClusterRoleBinding{}, Name: "system:firewall-policy-controller"},

		// droptailer
		{Type: &corev1.Namespace{}, Name: "firewall"},
		{Type: &appsv1.Deployment{}, Name: "droptailer"},

		// group rolebinding controller
		{Type: &rbacv1.ClusterRoleBinding{}, Name: "system:group-rolebinding-controller"},

		// ccm
		{Type: &rbacv1.ClusterRole{}, Name: "system:controller:cloud-node-controller"},
		{Type: &rbacv1.ClusterRoleBinding{}, Name: "system:controller:cloud-node-controller"},
		{Type: &rbacv1.ClusterRole{}, Name: "cloud-controller-manager"},
		{Type: &rbacv1.ClusterRoleBinding{}, Name: "cloud-controller-manager"},
	},
}

var storageClassChart = &chart.Chart{
	Name:   "shoot-storageclasses",
	Path:   filepath.Join(metal.InternalChartsPath, "shoot-storageclasses"),
	Images: []string{metal.CSIControllerImageName, metal.CSIProvisionerImageName},
	Objects: []*chart.Object{
		{Type: &corev1.Namespace{}, Name: "csi-lvm"},
		{Type: &storagev1.StorageClass{}, Name: "csi-lvm"},
		{Type: &corev1.ServiceAccount{}, Name: "csi-lvm-controller"},
		{Type: &rbacv1.ClusterRole{}, Name: "csi-lvm-controller"},
		{Type: &rbacv1.ClusterRoleBinding{}, Name: "csi-lvm-controller"},
		{Type: &appsv1.Deployment{}, Name: "csi-lvm-controller"},
		{Type: &corev1.ServiceAccount{}, Name: "csi-lvm-reviver"},
		{Type: &rbacv1.Role{}, Name: "csi-lvm-reviver"},
		{Type: &rbacv1.RoleBinding{}, Name: "csi-lvm-reviver"},
		{Type: &policyv1beta1.PodSecurityPolicy{}, Name: "csi-lvm-reviver-psp"},
		{Type: &rbacv1.Role{}, Name: "csi-lvm-reviver-psp"},
		{Type: &rbacv1.RoleBinding{}, Name: "csi-lvm-reviver-psp"},
		{Type: &appsv1.DaemonSet{}, Name: "csi-lvm-reviver"},
	},
}

// NewValuesProvider creates a new ValuesProvider for the generic actuator.
func NewValuesProvider(mgr manager.Manager, logger logr.Logger, accConfig AccountingConfig, authConfig AuthConfig) genericactuator.ValuesProvider {
	return &valuesProvider{
		mgr:              mgr,
		logger:           logger.WithName("metal-values-provider"),
		accountingConfig: accConfig,
		authConfig:       authConfig,
	}
}

// valuesProvider is a ValuesProvider that provides metal-specific values for the 2 charts applied by the generic actuator.
type valuesProvider struct {
	decoder          runtime.Decoder
	restConfig       *rest.Config
	client           client.Client
	logger           logr.Logger
	accountingConfig AccountingConfig
	authConfig       AuthConfig
	mgr              manager.Manager
}

// InjectScheme injects the given scheme into the valuesProvider.
func (vp *valuesProvider) InjectScheme(scheme *runtime.Scheme) error {
	vp.decoder = serializer.NewCodecFactory(scheme).UniversalDecoder()
	return nil
}

func (vp *valuesProvider) InjectConfig(restConfig *rest.Config) error {
	vp.restConfig = restConfig
	return nil
}

func (vp *valuesProvider) InjectClient(client client.Client) error {
	vp.client = client
	return nil
}

// GetConfigChartValues returns the values for the config chart applied by the generic actuator.
func (vp *valuesProvider) GetConfigChartValues(
	ctx context.Context,
	cp *extensionsv1alpha1.ControlPlane,
	cluster *extensionscontroller.Cluster,
) (map[string]interface{}, error) {

	values, err := vp.getAuthNConfigValues(ctx, cp, cluster)

	return values, err
}

func (vp *valuesProvider) getAuthNConfigValues(ctx context.Context, cp *extensionsv1alpha1.ControlPlane, cluster *extensionscontroller.Cluster) (map[string]interface{}, error) {

	namespace := cluster.Shoot.Status.TechnicalID

	// this should work as the kube-apiserver is a pod in the same cluster as the kube-jwt-authn-webhook
	// example https://kube-jwt-authn-webhook.shoot--local--myshootname.svc.cluster.local/authenticate
	url := fmt.Sprintf("https://%s.%s.svc.cluster.local/authenticate", authNWebhookDeploymentName, namespace)

	values := map[string]interface{}{
		"authnWebhook_url": url,
	}

	return values, nil
}

// GetControlPlaneChartValues returns the values for the control plane chart applied by the generic actuator.
func (vp *valuesProvider) GetControlPlaneChartValues(
	ctx context.Context,
	cp *extensionsv1alpha1.ControlPlane,
	cluster *extensionscontroller.Cluster,
	checksums map[string]string,
	scaledDown bool,
) (map[string]interface{}, error) {
	infrastructureConfig := &apismetal.InfrastructureConfig{}
	if _, _, err := vp.decoder.Decode(cluster.Shoot.Spec.Provider.InfrastructureConfig.Raw, nil, infrastructureConfig); err != nil {
		return nil, errors.Wrapf(err, "could not decode providerConfig of infrastructure")
	}

	cpConfig, err := helper.ControlPlaneConfigFromControlPlane(cp)
	if err != nil {
		return nil, err
	}

	cloudProfileConfig, err := helper.CloudProfileConfigFromCluster(cluster)
	if err != nil {
		return nil, err
	}

	cpConfig.IAMConfig, err = helper.MergeIAMConfig(cloudProfileConfig.IAMConfig, cpConfig.IAMConfig)
	if err != nil {
		return nil, err
	}

	mclient, err := metalclient.NewClient(ctx, vp.client, &cp.Spec.SecretRef)
	if err != nil {
		return nil, err
	}

	// Get CCM chart values
	chartValues, err := getCCMChartValues(cpConfig, infrastructureConfig, cp, cluster, checksums, scaledDown, mclient)
	if err != nil {
		return nil, err
	}

	authValues, err := getAuthNGroupRoleChartValues(cpConfig, cluster, vp.authConfig)
	if err != nil {
		return nil, err
	}

	accValues, err := getAccountingExporterChartValues(vp.accountingConfig, cluster, infrastructureConfig, mclient)
	if err != nil {
		return nil, err
	}

	lvwValues, err := getLimitValidationWebhookControlPlaneChartValues(cluster)
	if err != nil {
		return nil, err
	}

	merge(chartValues, authValues, accValues, lvwValues)

	return chartValues, nil
}

// merge all source maps in the target map
// hint: prevent overwriting of values due to duplicate keys by the use of prefixes
func merge(target map[string]interface{}, sources ...map[string]interface{}) {
	for sIndex := range sources {
		for k, v := range sources[sIndex] {
			target[k] = v
		}
	}
}

// GetControlPlaneExposureChartValues returns the values for the control plane exposure chart applied by the generic actuator.
func (vp *valuesProvider) GetControlPlaneExposureChartValues(
	ctx context.Context,
	cp *extensionsv1alpha1.ControlPlane,
	cluster *extensionscontroller.Cluster, m map[string]string) (map[string]interface{}, error) {
	return nil, nil
}

// GetControlPlaneShootChartValues returns the values for the control plane shoot chart applied by the generic actuator.
func (vp *valuesProvider) GetControlPlaneShootChartValues(ctx context.Context, cp *extensionsv1alpha1.ControlPlane, cluster *extensionscontroller.Cluster, checksums map[string]string) (map[string]interface{}, error) {
	vp.logger.Info("GetControlPlaneShootChartValues")

	values, err := vp.getControlPlaneShootLimitValidationWebhookChartValues(ctx, cp, cluster)
	if err != nil {
		vp.logger.Error(err, "Error getting LimitValidationWebhookChartValues")
		return nil, err
	}

	err = vp.deployControlPlaneShootDroptailerCerts(ctx, cp, cluster)
	if err != nil {
		vp.logger.Error(err, "error deploying droptailer certs")
	}

	return values, nil
}

// GetLimitValidationWebhookChartValues returns the values for the LimitValidationWebhook.
func (vp *valuesProvider) getControlPlaneShootLimitValidationWebhookChartValues(ctx context.Context, cp *extensionsv1alpha1.ControlPlane, cluster *extensionscontroller.Cluster) (map[string]interface{}, error) {
	secretName := limitValidatingWebhookServerName
	namespace := cluster.Shoot.Status.TechnicalID

	secret, err := vp.getSecret(ctx, namespace, secretName)
	if err != nil {
		return nil, err
	}

	// CA-Cert for TLS
	caBundle := base64.StdEncoding.EncodeToString(secret.Data[secrets.DataKeyCertificateCA])

	// this should work as the kube-apiserver is a pod in the same cluster as the limit-validating-webhook
	// example https://limit-validating-webhook.shoot--local--myshootname.svc.cluster.local/validate
	url := fmt.Sprintf("https://%s.%s.svc.cluster.local/validate", limitValidatingWebhookDeploymentName, namespace)

	values := map[string]interface{}{
		"limitValidatingWebhook_url":      url,
		"limitValidatingWebhook_caBundle": caBundle,
	}

	return values, nil
}

func (vp *valuesProvider) deployControlPlaneShootDroptailerCerts(ctx context.Context, cp *extensionsv1alpha1.ControlPlane, cluster *extensionscontroller.Cluster) error {
	// TODO: There is actually no nice way to deploy the certs into the shoot when we want to use
	// the certificate helper functions from Gardener itself...
	// Maybe we can find a better solution? This is actually only for chart values...

	wanted := &secrets.Secrets{
		CertificateSecretConfigs: map[string]*secrets.CertificateSecretConfig{
			v1alpha1constants.SecretNameCACluster: {
				Name:       v1alpha1constants.SecretNameCACluster,
				CommonName: "kubernetes",
				CertType:   secrets.CACert,
			},
		},
		SecretConfigsFunc: func(cas map[string]*secrets.Certificate, clusterName string) []secrets.ConfigInterface {
			return []secrets.ConfigInterface{
				&secrets.ControlPlaneSecretConfig{
					CertificateSecretConfig: &secrets.CertificateSecretConfig{
						Name:         droptailerClientSecretName,
						CommonName:   "droptailer",
						Organization: []string{"droptailer-client"},
						CertType:     secrets.ClientCert,
						SigningCA:    cas[v1alpha1constants.SecretNameCACluster],
					},
				},
				&secrets.ControlPlaneSecretConfig{
					CertificateSecretConfig: &secrets.CertificateSecretConfig{
						Name:         droptailerServerSecretName,
						CommonName:   "droptailer",
						Organization: []string{"droptailer-server"},
						CertType:     secrets.ServerCert,
						SigningCA:    cas[v1alpha1constants.SecretNameCACluster],
					},
				},
			}
		},
	}

	shootConfig, _, err := util.NewClientForShoot(ctx, vp.client, cluster.Shoot.Status.TechnicalID, client.Options{})
	if err != nil {
		return errors.Wrap(err, "could not create shoot client")
	}
	cs, err := kubernetes.NewForConfig(shootConfig)
	if err != nil {
		return errors.Wrap(err, "could not create shoot kubernetes client")
	}
	gcs, err := gardenerkubernetes.NewWithConfig(gardenerkubernetes.WithRESTConfig(shootConfig))
	if err != nil {
		return errors.Wrap(err, "could not create shoot Gardener client")
	}

	_, err = cs.CoreV1().Namespaces().Get(droptailerNamespace, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: droptailerNamespace,
				},
			}
			_, err := cs.CoreV1().Namespaces().Create(ns)
			if err != nil {
				return errors.Wrap(err, "could not create droptailer namespace")
			}
		} else {
			return errors.Wrap(err, "could not search for existence of droptailer namespace")
		}
	}

	_, err = wanted.Deploy(ctx, cs, gcs, droptailerNamespace)
	if err != nil {
		return errors.Wrap(err, "could not deploy droptailer secrets to shoot cluster")
	}

	return nil
}

// getSecret returns the secret with the given namespace/secretName
func (vp *valuesProvider) getSecret(ctx context.Context, namespace string, secretName string) (*corev1.Secret, error) {
	key := kutil.Key(namespace, secretName)
	vp.logger.Info("GetSecret", "key", key)
	secret := &corev1.Secret{}
	err := vp.mgr.GetClient().Get(ctx, key, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			vp.logger.Error(err, "error getting chart secret - not found")
			return nil, err
		}
		vp.logger.Error(err, "error getting chart secret")
		return nil, err
	}
	return secret, nil
}

// GetStorageClassesChartValues returns the values for the storage classes chart applied by the generic actuator.
func (vp *valuesProvider) GetStorageClassesChartValues(context.Context, *extensionsv1alpha1.ControlPlane, *extensionscontroller.Cluster) (map[string]interface{}, error) {
	return nil, nil
}

// getCCMChartValues collects and returns the CCM chart values.
func getCCMChartValues(
	cpConfig *apismetal.ControlPlaneConfig,
	infrastructure *apismetal.InfrastructureConfig,
	cp *extensionsv1alpha1.ControlPlane,
	cluster *extensionscontroller.Cluster,
	checksums map[string]string,
	scaledDown bool,
	mclient *metalgo.Driver,
) (map[string]interface{}, error) {
	projectID := infrastructure.ProjectID
	nodeCIDR := cluster.Shoot.Spec.Networking.Nodes

	if nodeCIDR == nil {
		return nil, fmt.Errorf("nodeCIDR was not yet set by infrastructure controller")
	}

	privateNetwork, err := metalclient.GetPrivateNetworkFromNodeNetwork(mclient, projectID, *nodeCIDR)
	if err != nil {
		return nil, err
	}

	values := map[string]interface{}{
		"replicas":          extensionscontroller.GetControlPlaneReplicas(cluster, scaledDown, 1),
		"projectID":         projectID,
		"clusterID":         cluster.Shoot.ObjectMeta.UID,
		"partitionID":       infrastructure.PartitionID,
		"networkID":         *privateNetwork.ID,
		"kubernetesVersion": cluster.Shoot.Spec.Kubernetes.Version,
		"podNetwork":        extensionscontroller.GetPodNetwork(cluster),
		"podAnnotations": map[string]interface{}{
			"checksum/secret-cloud-controller-manager":        checksums[cloudControllerManagerDeploymentName],
			"checksum/secret-cloud-controller-manager-server": checksums[cloudControllerManagerServerName],
			// TODO Use constant from github.com/gardener/gardener/pkg/apis/core/v1alpha1 when available
			// See https://github.com/gardener/gardener/pull/930
			"checksum/secret-cloudprovider":            checksums[v1alpha1constants.SecretNameCloudProvider],
			"checksum/configmap-cloud-provider-config": checksums[metal.CloudProviderConfigName],
		},
	}

	if cpConfig.CloudControllerManager != nil {
		values["featureGates"] = cpConfig.CloudControllerManager.FeatureGates
	}

	return values, nil
}

// returns values for "authn-webhook" and "group-rolebinding-controller" that are thematically related
func getAuthNGroupRoleChartValues(cpConfig *apismetal.ControlPlaneConfig, cluster *extensionscontroller.Cluster, authConfig AuthConfig) (map[string]interface{}, error) {

	annotations := cluster.Shoot.GetAnnotations()
	clusterName := annotations[tag.ClusterName]
	tenant := annotations[tag.ClusterTenant]

	ti := cpConfig.IAMConfig.IssuerConfig

	values := map[string]interface{}{
		"authn_tenant":             tenant,
		"authn_clustername":        clusterName,
		"authn_oidcIssuerUrl":      ti.Url,
		"authn_oidcIssuerClientId": ti.ClientId,
		"authn_debug":              "true",
		"authn_providerTenant":     authConfig.ProviderTenant,

		"grprb_clustername": clusterName,
	}

	return values, nil
}

func getAccountingExporterChartValues(accountingConfig AccountingConfig, cluster *extensionscontroller.Cluster, infrastructure *apismetal.InfrastructureConfig, mclient *metalgo.Driver) (map[string]interface{}, error) {
	annotations := cluster.Shoot.GetAnnotations()
	partitionID := infrastructure.PartitionID
	projectID := infrastructure.ProjectID
	clusterID := cluster.Shoot.ObjectMeta.UID
	clusterName := annotations[tag.ClusterName]
	tenant := annotations[tag.ClusterTenant]

	resp, err := mclient.ProjectGet(projectID)
	if err != nil {
		return nil, err
	}
	project := resp.Project

	values := map[string]interface{}{
		"accex_partitionID": partitionID,
		"accex_tenant":      tenant,
		"accex_projectname": project.Name,
		"accex_projectID":   projectID,

		"accex_clustername": clusterName,
		"accex_clusterID":   clusterID,

		"accex_accountingsink_url":  accountingConfig.AccountingSinkUrl,
		"accex_accountingsink_HMAC": accountingConfig.AccountingSinkHmac,
	}

	return values, nil
}

func getLimitValidationWebhookControlPlaneChartValues(cluster *extensionscontroller.Cluster) (map[string]interface{}, error) {

	// limit validation deactivated
	// import helper "github.com/gardener/gardener/pkg/apis/garden/v1beta1/helper"
	// shootedSeed, err := helper.ReadShootedSeed(cluster.Shoot)
	// isNormalShoot := shootedSeed == nil || err != nil

	values := map[string]interface{}{
		"lvw_validate": false,
	}

	return values, nil
}

// Data for configuration of IDM-API WebHook (deployment to be done!)
type UserDirectory struct {
	IdmApi           string `json:"idmApi" optional:"false"`
	IdmApiUser       string `json:"idmApiUser" optional:"false"`
	IdmApiPassword   string `json:"idmApiPassword" optional:"false"`
	TargetSystemId   string `json:"targetSystemId" optional:"false"`
	TargetSystemType string `json:"targetSystemType" optional:"false"`
	AccessCode       string `json:"accessCode" optional:"false"`
	CustomerId       string `json:"cstomerId" optional:"false"`
}
