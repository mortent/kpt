package rootsyncset

import (
	"context"
	"encoding/base64"
	"fmt"

	container "cloud.google.com/go/container/apiv1"
	"github.com/GoogleContainerTools/kpt/porch/controllers/rootsyncsets/api/v1alpha1"
	"github.com/GoogleContainerTools/kpt/porch/pkg/googleurl"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	containerpb "google.golang.org/genproto/googleapis/container/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClientFactory struct {
	client                 client.Client
	workloadIdentityHelper WorkloadIdentityHelper
}

func (cf *ClientFactory) New(ctx context.Context, ref v1alpha1.ClusterRef, ns string) (dynamic.Interface, error) {
	key := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
	if key.Namespace == "" {
		key.Namespace = ns
	}
	u := &unstructured.Unstructured{}
	var config *rest.Config
	gv, err := schema.ParseGroupVersion(ref.ApiVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to parse group version when building object: %w", err)
	}

	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    ref.Kind,
	})
	if err := cf.client.Get(ctx, key, u); err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}
	if ref.Kind == containerClusterKind {
		config, err = cf.GetGKERESTConfig(ctx, u)
	} else if ref.Kind == configControllerKind {
		config, err = cf.GetCCRESTConfig(ctx, u) //TODO: tmp workaround, update after ACP add new fields
	} else {
		return nil, fmt.Errorf("failed to find target cluster, cluster kind has to be ContainerCluster or ConfigControllerInstance")
	}
	if err != nil {
		return nil, err
	}
	myClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create a new dynamic client: %w", err)
	}
	return myClient, nil
}

// GetGKERESTConfig builds a rest.Config for accessing the specified cluster,
// without assuming that kubeconfig is correctly configured / mapped.
func (cf *ClientFactory) GetGKERESTConfig(ctx context.Context, cluster *unstructured.Unstructured) (*rest.Config, error) {
	restConfig := &rest.Config{}
	clusterCaCertificate, exist, err := unstructured.NestedString(cluster.Object, "spec", "masterAuth", "clusterCaCertificate")
	if err != nil {
		return nil, fmt.Errorf("failed to get rest config: %w", err)
	}
	if !exist {
		return nil, fmt.Errorf("clusterCaCertificate field does not exist")
	}
	caData, err := base64.StdEncoding.DecodeString(clusterCaCertificate)
	if err != nil {
		return nil, fmt.Errorf("error decoding ca certificate: %w", err)
	}
	restConfig.CAData = caData
	endpoint, exist, err := unstructured.NestedString(cluster.Object, "status", "endpoint")
	if err != nil {
		return nil, fmt.Errorf("failed to get rest config: %w", err)
	}
	if !exist {
		return nil, fmt.Errorf("endpoint field does not exist")
	}
	restConfig.Host = "https://" + endpoint
	klog.Infof("Host endpoint is %s", restConfig.Host)
	tokenSource, err := cf.GetConfigConnectorContextTokenSource(ctx, cluster.GetNamespace())
	if err != nil {
		return nil, err
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("error getting token: %w", err)
	}
	restConfig.BearerToken = token.AccessToken
	return restConfig, nil
}

// GetConfigConnectorContextTokenSource gets and returns the ConfigConnectorContext for the given namespace.
func (cf *ClientFactory) GetConfigConnectorContextTokenSource(ctx context.Context, ns string) (oauth2.TokenSource, error) {
	gvr := schema.GroupVersionResource{
		Group:    "core.cnrm.cloud.google.com",
		Version:  "v1beta1",
		Resource: "configconnectorcontexts",
	}

	cr, err := cf.workloadIdentityHelper.dynamicClient.Resource(gvr).Namespace(ns).Get(ctx, "configconnectorcontext.core.cnrm.cloud.google.com", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	googleServiceAccount, _, err := unstructured.NestedString(cr.Object, "spec", "googleServiceAccount")
	if err != nil {
		return nil, fmt.Errorf("error reading spec.googleServiceAccount from ConfigConnectorContext in %q: %w", ns, err)
	}

	if googleServiceAccount == "" {
		return nil, fmt.Errorf("could not find spec.googleServiceAccount from ConfigConnectorContext in %q: %w", ns, err)
	}

	kubeServiceAccount := types.NamespacedName{
		Namespace: "cnrm-system",
		Name:      "cnrm-controller-manager-" + ns,
	}
	return cf.workloadIdentityHelper.GetGcloudAccessTokenSource(ctx, kubeServiceAccount, googleServiceAccount)
}

// GetCCRESTConfig builds a rest.Config for accessing the config controller cluster,
// this is a tmp workaround.
func (cf *ClientFactory) GetCCRESTConfig(ctx context.Context, cluster *unstructured.Unstructured) (*rest.Config, error) {
	gkeResourceLink, _, err := unstructured.NestedString(cluster.Object, "status", "gkeResourceLink")
	if err != nil {
		return nil, fmt.Errorf("failed to get status.gkeResourceLink field: %w", err)
	}
	if gkeResourceLink == "" {
		return nil, fmt.Errorf("status.gkeResourceLink not set in object")
	}

	googleURL, err := googleurl.ParseUnversioned(gkeResourceLink)
	if err != nil {
		return nil, fmt.Errorf("error parsing gkeResourceLink %q: %w", gkeResourceLink, err)
	}
	projectID := googleURL.Project
	location := googleURL.Location
	clusterName := googleURL.Extra["clusters"]

	tokenSource, err := cf.GetConfigConnectorContextTokenSource(ctx, cluster.GetNamespace())
	if err != nil {
		return nil, err
	}

	c, err := container.NewClusterManagerClient(ctx, option.WithTokenSource(tokenSource), option.WithQuotaProject(projectID))
	if err != nil {
		return nil, fmt.Errorf("failed to create new cluster manager client: %w", err)
	}
	defer c.Close()

	clusterSelfLink := "projects/" + projectID + "/locations/" + location + "/clusters/" + clusterName
	klog.Infof("cluster path is %s", clusterSelfLink)
	req := &containerpb.GetClusterRequest{
		Name: clusterSelfLink,
	}
	resp, err := c.GetCluster(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster info for cluster %q: %w", clusterSelfLink, err)
	}
	restConfig := &rest.Config{}
	caData, err := base64.StdEncoding.DecodeString(resp.MasterAuth.ClusterCaCertificate)
	if err != nil {
		return nil, fmt.Errorf("error decoding ca certificate from gke cluster %q: %w", clusterSelfLink, err)
	}
	restConfig.CAData = caData

	restConfig.Host = "https://" + resp.Endpoint
	klog.Infof("Host endpoint is %s", restConfig.Host)

	token, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("error getting token: %w", err)
	}

	restConfig.BearerToken = token.AccessToken
	return restConfig, nil
}
