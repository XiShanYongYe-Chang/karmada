package e2e

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"

	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	"github.com/karmada-io/karmada/test/e2e/framework"
	testhelper "github.com/karmada-io/karmada/test/helper"
)

// BasicPropagation focus on basic propagation functionality testing.
var _ = ginkgo.Describe("namespace scope resource propagation quantity testing", func() {
	var deploymentSlice []*appsv1.Deployment
	var policySlice []*policyv1alpha1.PropagationPolicy
	var quantityNum int
	var targetClusters []string

	ginkgo.BeforeEach(func() {
		quantityNum = 10
		targetClusters = []string{"member1"}

		policySlice = make([]*policyv1alpha1.PropagationPolicy, quantityNum)
		deploymentSlice = make([]*appsv1.Deployment, quantityNum)

		for index := 0; index < quantityNum; index++ {
			deploymentName := fmt.Sprintf("%s-%d", deploymentNamePrefix, index)

			deployment := testhelper.NewDeployment(testNamespace, deploymentName)
			policy := testhelper.NewPropagationPolicy(testNamespace, deploymentName, []policyv1alpha1.ResourceSelector{
				{
					APIVersion: deployment.APIVersion,
					Kind:       deployment.Kind,
					Name:       deployment.Name,
				},
			}, policyv1alpha1.Placement{
				ClusterAffinity: &policyv1alpha1.ClusterAffinity{
					ClusterNames: targetClusters,
				},
			})

			deploymentSlice[index] = deployment
			policySlice[index] = policy
		}
	})

	ginkgo.BeforeEach(func() {
		for index := 0; index < quantityNum; index++ {
			deployment := deploymentSlice[index]
			policy := policySlice[index]

			framework.CreatePropagationPolicy(karmadaClient, policy)
			framework.CreateDeployment(kubeClient, deployment)
			ginkgo.DeferCleanup(func() {
				framework.RemovePropagationPolicy(karmadaClient, policy.Namespace, policy.Name)
				framework.RemoveDeployment(kubeClient, deployment.Namespace, deployment.Name)
			})
		}
	})

	ginkgo.It("deployment propagation testing", func() {
		for _, cluster := range targetClusters {
			clusterClient := framework.GetClusterClient(cluster)
			gomega.Expect(clusterClient).ShouldNot(gomega.BeNil())

			gomega.Eventually(func() bool {
				deploys, err := clusterClient.AppsV1().Deployments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					return false
				}

				return len(deploys.Items) == quantityNum
			}, pollTimeout, pollInterval).Should(gomega.Equal(true))
		}
	})
})

var _ = ginkgo.Describe("cluster scope resource propagation quantity testing", func() {
	ginkgo.Context("CustomResourceDefinition propagation testing", func() {
		var crdGroup string
		var randStr string
		var crdSpecNames apiextensionsv1.CustomResourceDefinitionNames
		var crd *apiextensionsv1.CustomResourceDefinition
		var crdPolicy *policyv1alpha1.ClusterPropagationPolicy

		ginkgo.BeforeEach(func() {
			crdGroup = fmt.Sprintf("example-%s.karmada.io", rand.String(RandomStrLength))
			randStr = rand.String(RandomStrLength)
			crdSpecNames = apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     fmt.Sprintf("Foo%s", randStr),
				ListKind: fmt.Sprintf("Foo%sList", randStr),
				Plural:   fmt.Sprintf("foo%ss", randStr),
				Singular: fmt.Sprintf("foo%s", randStr),
			}
			crd = testhelper.NewCustomResourceDefinition(crdGroup, crdSpecNames, apiextensionsv1.NamespaceScoped)
			crdPolicy = testhelper.NewClusterPropagationPolicy(crd.Name, []policyv1alpha1.ResourceSelector{
				{
					APIVersion: crd.APIVersion,
					Kind:       crd.Kind,
					Name:       crd.Name,
				},
			}, policyv1alpha1.Placement{
				ClusterAffinity: &policyv1alpha1.ClusterAffinity{
					ClusterNames: framework.ClusterNames(),
				},
			})
		})

		ginkgo.BeforeEach(func() {
			framework.CreateClusterPropagationPolicy(karmadaClient, crdPolicy)
			framework.CreateCRD(dynamicClient, crd)
			ginkgo.DeferCleanup(func() {
				framework.RemoveClusterPropagationPolicy(karmadaClient, crdPolicy.Name)
				framework.RemoveCRD(dynamicClient, crd.Name)
				framework.WaitCRDDisappearedOnClusters(framework.ClusterNames(), crd.Name)
			})
		})

		ginkgo.It("crd propagation testing", func() {
			framework.GetCRD(dynamicClient, crd.Name)
			framework.WaitCRDPresentOnClusters(karmadaClient, framework.ClusterNames(),
				fmt.Sprintf("%s/%s", crd.Spec.Group, "v1alpha1"), crd.Spec.Names.Kind)
		})
	})

})
