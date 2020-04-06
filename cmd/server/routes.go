package main

//
// Route handlers for the API, does the actual k8s data scraping
// Ben Coleman, July 2019, v1
//

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/benc-uk/go-starter/pkg/envhelper"
	"github.com/gorilla/mux"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	v1beta1 "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Config is simple structure returned by routeConfig
type Config struct {
	NamespaceScope string
}

// Data struct to hold our returned data
type scrapeData struct {
	Pods                   []apiv1.Pod                   `json:"pods"`
	Services               []apiv1.Service               `json:"services"`
	Endpoints              []apiv1.Endpoints             `json:"endpoints"`
	PersistentVolumes      []apiv1.PersistentVolume      `json:"persistentvolumes"`
	PersistentVolumeClaims []apiv1.PersistentVolumeClaim `json:"persistentvolumeclaims"`
	Deployments            []appsv1.Deployment           `json:"deployments"`
	DaemonSets             []appsv1.DaemonSet            `json:"daemonsets"`
	ReplicaSets            []appsv1.ReplicaSet           `json:"replicasets"`
	StatefulSets           []appsv1.StatefulSet          `json:"statefulsets"`
	Ingresses              []v1beta1.Ingress             `json:"ingresses"`
	ConfigMaps             []apiv1.ConfigMap             `json:"configmaps"`
	Secrets                []apiv1.Secret                `json:"secrets"`
}

//
// Simple health check endpoint, returns 204 when healthy
//
func routeHealthCheck(resp http.ResponseWriter, req *http.Request) {
	if healthy {
		resp.WriteHeader(http.StatusNoContent)
		return
	}
	resp.WriteHeader(http.StatusServiceUnavailable)
}

//
// Return status information data
//
func routeStatus(resp http.ResponseWriter, req *http.Request) {
	type status struct {
		Healthy    bool   `json:"healthy"`
		Version    string `json:"version"`
		BuildInfo  string `json:"buildInfo"`
		Hostname   string `json:"hostname"`
		OS         string `json:"os"`
		Arch       string `json:"architecture"`
		CPU        int    `json:"cpuCount"`
		GoVersion  string `json:"goVersion"`
		ClientAddr string `json:"clientAddress"`
		ServerHost string `json:"serverHost"`
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "hostname not available"
	}

	currentStatus := status{
		Healthy:    healthy,
		Version:    version,
		BuildInfo:  buildInfo,
		Hostname:   hostname,
		GoVersion:  runtime.Version(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		CPU:        runtime.NumCPU(),
		ClientAddr: req.RemoteAddr,
		ServerHost: req.Host,
	}

	statusJSON, err := json.Marshal(currentStatus)
	if err != nil {
		http.Error(resp, "Failed to get status", http.StatusInternalServerError)
	}

	resp.Header().Add("Content-Type", "application/json")
	resp.Write(statusJSON)
}

//
// Return list of all namespaces in cluster
//
func routeGetNamespaces(resp http.ResponseWriter, req *http.Request) {
	namespaces, err := clientset.CoreV1().Namespaces().List(metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
	}
	namespacesJSON, _ := json.Marshal(namespaces.Items)
	resp.Header().Set("Access-Control-Allow-Origin", "*")
	resp.Header().Add("Content-Type", "application/json")
	resp.Write(namespacesJSON)
}

//
// Return aggregated data from loads of different Kubernetes object types
//
func routeScrapeData(resp http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	namespace := params["ns"]

	pods, err := clientset.CoreV1().Pods(namespace).List(metav1.ListOptions{})
	services, err := clientset.CoreV1().Services(namespace).List(metav1.ListOptions{})
	endpoints, err := clientset.CoreV1().Endpoints(namespace).List(metav1.ListOptions{})
	pvs, err := clientset.CoreV1().PersistentVolumes().List(metav1.ListOptions{})
	pvcs, err := clientset.CoreV1().PersistentVolumeClaims(namespace).List(metav1.ListOptions{})
	configmaps, err := clientset.CoreV1().ConfigMaps(namespace).List(metav1.ListOptions{})
	secrets, err := clientset.CoreV1().Secrets(namespace).List(metav1.ListOptions{})
	deployments, err := clientset.AppsV1().Deployments(namespace).List(metav1.ListOptions{})
	daemonsets, err := clientset.AppsV1().DaemonSets(namespace).List(metav1.ListOptions{})
	replicasets, err := clientset.AppsV1().ReplicaSets(namespace).List(metav1.ListOptions{})
	statefulsets, err := clientset.AppsV1().StatefulSets(namespace).List(metav1.ListOptions{})

	ingresses, err := clientset.ExtensionsV1beta1().Ingresses(namespace).List(metav1.ListOptions{})

	if err != nil {
		log.Println("### Kubernetes API error", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	// Remove and hide Helm v3 release secrets, we're never going to show them
	secrets.Items = filterSecrets(secrets.Items, func(v apiv1.Secret) bool {
		return !strings.HasPrefix(v.ObjectMeta.Name, "sh.helm.release")
	})

	// Obfuscate & remove secret values
	for _, secret := range secrets.Items {
		// Inside 'last-applied-configuration'
		if secret.ObjectMeta.Annotations["kubectl.kubernetes.io/last-applied-configuration"] != "" {
			secret.ObjectMeta.Annotations["kubectl.kubernetes.io/last-applied-configuration"] = "__VALUE REDACTED__"
		}

		// And the data values of course
		for key := range secret.Data {
			secret.Data[key] = []byte("__VALUE REDACTED__")
		}
	}

	// Dump of results
	scrapeResult := scrapeData{
		Pods:                   pods.Items,
		Services:               services.Items,
		Endpoints:              endpoints.Items,
		PersistentVolumes:      pvs.Items,
		PersistentVolumeClaims: pvcs.Items,
		Deployments:            deployments.Items,
		DaemonSets:             daemonsets.Items,
		ReplicaSets:            replicasets.Items,
		StatefulSets:           statefulsets.Items,
		Ingresses:              ingresses.Items,
		ConfigMaps:             configmaps.Items,
		Secrets:                secrets.Items,
	}

	scrapeResultJSON, _ := json.Marshal(scrapeResult)
	resp.Header().Set("Access-Control-Allow-Origin", "*")
	resp.Header().Add("Content-Type", "application/json")
	resp.Write([]byte(scrapeResultJSON))
}

//
// Simple config endpoint, returns NAMESPACE_SCOPE var to front end
//
func routeConfig(resp http.ResponseWriter, req *http.Request) {
	nsScope := envhelper.GetEnvString("NAMESPACE_SCOPE", "*")
	conf := Config{NamespaceScope: nsScope}

	configJSON, _ := json.Marshal(conf)
	resp.Header().Set("Access-Control-Allow-Origin", "*")
	resp.Header().Add("Content-Type", "application/json")
	resp.Write([]byte(configJSON))
}

//
// Filter a slice of Secrets
//
func filterSecrets(secretList []apiv1.Secret, f func(apiv1.Secret) bool) []apiv1.Secret {
	newSlice := make([]apiv1.Secret, 0)
	for _, secret := range secretList {
		if f(secret) {
			newSlice = append(newSlice, secret)
		}
	}
	return newSlice
}
