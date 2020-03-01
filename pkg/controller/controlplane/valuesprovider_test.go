package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	extensionscontroller "github.com/gardener/gardener-extensions/pkg/controller"
	"github.com/go-openapi/strfmt"
	"github.com/metal-pod/metal-go/api/models"
	cloudmodels "github.com/metal-stack/cloud-go/api/models"
	apismetal "github.com/metal-stack/gardener-extension-provider-metal/pkg/apis/metal"

	"github.com/metal-stack/gardener-extension-provider-metal/pkg/metal"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"

	"github.com/golang/glog"
	"github.com/golang/mock/gomock"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	controllers "sigs.k8s.io/controller-runtime"

	mockclient "github.com/gardener/gardener-extensions/pkg/mock/controller-runtime/client"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/runtime/inject"
)

const (
	namespace = "test"
)

var _ = Describe("ValuesProvider", func() {
	var (
		ctrl *gomock.Controller

		// Build scheme
		scheme = runtime.NewScheme()
		_      = apismetal.AddToScheme(scheme)

		cpSecretKey = client.ObjectKey{Namespace: namespace, Name: v1beta1constants.SecretNameCloudProvider}
		cpSecret    = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      v1beta1constants.SecretNameCloudProvider,
				Namespace: namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"metalAPIHMac": []byte(`cdf`),
				"metalAPIURL":  []byte(`http://localhost:8888/`),
				"cloudAPIHMac": []byte(`cdf`),
				"cloudAPIURL":  []byte(`http://localhost:8888/`),
			},
		}

		cp = &extensionsv1alpha1.ControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "control-plane",
				Namespace: namespace,
			},
			Spec: extensionsv1alpha1.ControlPlaneSpec{
				DefaultSpec: extensionsv1alpha1.DefaultSpec{
					ProviderConfig: &runtime.RawExtension{
						Raw: encode(&apismetal.ControlPlaneConfig{
							CloudControllerManager: &apismetal.CloudControllerManagerConfig{
								FeatureGates: map[string]bool{
									"CustomResourceValidation": true,
								},
							},
							IAMConfig: &apismetal.IAMConfig{
								IssuerConfig: &apismetal.IssuerConfig{
									Url:      "http://dex/",
									ClientId: "auth-go-cli",
								},
								IdmConfig: &apismetal.IDMConfig{
									Idmtype: "UX",
								},
								GroupConfig: &apismetal.NamespaceGroupConfig{
									NamespaceMaxLength: 20,
								},
							},
						}),
					},
				},
				InfrastructureProviderStatus: &runtime.RawExtension{
					Raw: encode(&apismetal.InfrastructureStatus{
						Firewall: apismetal.FirewallStatus{
							Succeeded: true,
							MachineID: "9b000000-0000-0000-0000-000000000001",
						},
					}),
				},
				SecretRef: corev1.SecretReference{
					Name:      "cloudprovider",
					Namespace: "test",
				},
			},
		}

		cidr    = ("10.250.0.0/19")
		cluster = &extensionscontroller.Cluster{
			CloudProfile: &gardencorev1beta1.CloudProfile{
				Spec: gardencorev1beta1.CloudProfileSpec{
					ProviderConfig: &gardencorev1beta1.ProviderConfig{
						runtime.RawExtension{
							Raw: encode(&apismetal.CloudProfileConfig{
								IAMConfig: &apismetal.IAMConfig{
									IssuerConfig: &apismetal.IssuerConfig{
										Url:      "http://dex/",
										ClientId: "auth-go-cli",
									},
									IdmConfig: &apismetal.IDMConfig{
										Idmtype: "UX",
									},
									GroupConfig: &apismetal.NamespaceGroupConfig{
										NamespaceMaxLength: 20,
									},
								},
							}),
						},
					},
				},
			},
			Shoot: &gardencorev1beta1.Shoot{
				Spec: gardencorev1beta1.ShootSpec{
					Provider: gardencorev1beta1.Provider{
						InfrastructureConfig: &gardencorev1beta1.ProviderConfig{
							runtime.RawExtension{
								Raw: encode(&apismetal.InfrastructureConfig{
									Firewall: apismetal.Firewall{
										Size:     "c1-xlarge-x86",
										Image:    "firewall-1",
										Networks: []string{"internet-nbg-w8101"},
									},
									PartitionID: "partition",
									ProjectID:   "project1",
								}),
							},
						},
					},
					Networking: gardencorev1beta1.Networking{
						Pods:  &cidr,
						Nodes: &cidr,
					},
					Kubernetes: gardencorev1beta1.Kubernetes{
						Version: "1.13.4",
					},
				},
			},
		}

		checksums = map[string]string{
			v1beta1constants.SecretNameCloudProvider: "8bafb35ff1ac60275d62e1cbd495aceb511fb354f74a20f7d06ecb48b3a68432",
			metal.CloudProviderConfigName:            "08a7bc7fe8f59b055f173145e211760a83f02cf89635cef26ebb351378635606",
			"cloud-controller-manager":               "3d791b164a808638da9a8df03924be2a41e34cd664e42231c00fe369e3588272",
			"cloud-controller-manager-server":        "6dff2a2e6f14444b66d8e4a351c049f7e89ee24ba3eaab95dbec40ba6bdebb52",
		}

		configChartValues = map[string]interface{}{
			"authnWebhook_url": "https://kube-jwt-authn-webhook..svc.cluster.local/authenticate",
		}

		ccmChartValues = map[string]interface{}{
			"accex_tenant":              "",
			"clusterID":                 "",
			"authn_tenant":              "",
			"authn_oidcIssuerClientId":  "auth-go-cli",
			"grprb_clustername":         "",
			"accex_accountingsink_url":  "http://localhost:8888/",
			"accex_accountingsink_HMAC": "_dummy_",
			"accex_partitionID":         "partition",
			"lvw_validate":              false,
			"replicas":                  1,
			"kubernetesVersion":         "1.13.4",
			"podAnnotations": map[string]interface{}{
				"checksum/secret-cloud-controller-manager":        "3d791b164a808638da9a8df03924be2a41e34cd664e42231c00fe369e3588272",
				"checksum/secret-cloud-controller-manager-server": "6dff2a2e6f14444b66d8e4a351c049f7e89ee24ba3eaab95dbec40ba6bdebb52",
				"checksum/secret-cloudprovider":                   "8bafb35ff1ac60275d62e1cbd495aceb511fb354f74a20f7d06ecb48b3a68432",
				"checksum/configmap-cloud-provider-config":        "08a7bc7fe8f59b055f173145e211760a83f02cf89635cef26ebb351378635606",
			},
			"accex_projectID":   "project1",
			"accex_clustername": "",
			"podNetwork":        "10.250.0.0/19",
			"featureGates": map[string]bool{
				"CustomResourceValidation": true,
			},
			"authn_debug":         "true",
			"authn_clustername":   "",
			"authn_oidcIssuerUrl": "http://dex/",
			"accex_clusterID":     "",
			"accex_projectname":   "project1",
			"projectID":           "project1",
			"partitionID":         "partition",
			"networkID":           "myid",
		}

		logger = log.Log.WithName("test")

		mgr, _ = controllers.NewManager(controllers.GetConfigOrDie(), controllers.Options{})

		ac = AccountingConfig{
			AccountingSinkUrl:  "http://localhost:8888/",
			AccountingSinkHmac: "_dummy_",
		}
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	Describe("#GetConfigChartValues", func() {
		It("should return correct config chart values", func() {
			// Create valuesProvider
			vp := NewValuesProvider(mgr, logger, AccountingConfig{})
			err := vp.(inject.Scheme).InjectScheme(scheme)
			Expect(err).NotTo(HaveOccurred())

			// Call GetConfigChartValues method and check the result
			values, err := vp.GetConfigChartValues(context.TODO(), cp, cluster)
			Expect(err).NotTo(HaveOccurred())
			Expect(values).To(Equal(configChartValues))
		})
	})

	Describe("#GetControlPlaneChartValues", func() {
		It("should return correct control plane chart values", func() {
			// Create valuesProvider
			vp := NewValuesProvider(mgr, logger, ac)
			err := vp.(inject.Scheme).InjectScheme(scheme)
			Expect(err).NotTo(HaveOccurred())

			// Create mock client
			client := mockclient.NewMockClient(ctrl)

			client.EXPECT().Get(context.TODO(), cpSecretKey, &corev1.Secret{}).DoAndReturn(clientGet(cpSecret)).AnyTimes()
			go mockMetalApi()

			err = vp.(inject.Client).InjectClient(client)
			Expect(err).To(Not(HaveOccurred()))

			// Call GetControlPlaneChartValues method and check the result
			values, err := vp.GetControlPlaneChartValues(context.TODO(), cp, cluster, checksums, false)
			Expect(err).NotTo(HaveOccurred())
			jv, _ := json.Marshal(values)
			jc, _ := json.Marshal(ccmChartValues)
			Expect(jv).To(MatchJSON(jc))
		})
	})
})

func encode(obj runtime.Object) []byte {
	data, _ := json.Marshal(obj)
	return data
}

func clientGet(result runtime.Object) interface{} {
	return func(ctx context.Context, key client.ObjectKey, obj runtime.Object) error {
		switch obj.(type) {
		case *corev1.Secret:
			*obj.(*corev1.Secret) = *result.(*corev1.Secret)
		}
		return nil
	}
}

func mockMetalApi() {

	var (
		ID = "myid"
		f  = false
		i  = int64(64)
		nr = models.V1NetworkResponse{
			Changed:             strfmt.DateTime(time.Now()),
			Created:             strfmt.DateTime(time.Now()),
			ID:                  &ID,
			Destinationprefixes: []string{"10.0.0.0/22"},
			Labels:              map[string]string{"dummy": "label"},
			Privatesuper:        &f,
			Underlay:            &f,
			Parentnetworkid:     &ID,
			Nat:                 &f,
			Usage: &models.V1NetworkUsage{
				AvailableIps:      &i,
				AvailablePrefixes: &i,
				UsedIps:           &i,
				UsedPrefixes:      &i,
			},
		}

		pr1 = cloudmodels.V1ProjectResponse{
			Project: &cloudmodels.V1Project{
				Meta: &cloudmodels.V1Meta{
					ID:          "project1",
					UpdatedTime: strfmt.DateTime(time.Now()),
					CreatedTime: strfmt.DateTime(time.Now()),
				},
				Name:     "project1",
				TenantID: "tenent",
			},
		}
	)

	http.HandleFunc("/v1/network/find", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		resp, err := json.Marshal([]models.V1NetworkResponse{nr})
		if err != nil {
			return
		}
		w.Write(resp)
	})
	http.HandleFunc("/v1/project/project1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		resp, err := json.Marshal(pr1)
		if err != nil {
			return
		}
		w.Write(resp)
	})
	glog.Fatal(http.ListenAndServe(":8888", nil))
}
